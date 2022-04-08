package nodepool

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"time"

	api "github.com/openshift/hypershift/api"
	hyperv1 "github.com/openshift/hypershift/api/v1alpha1"
	hyperutil "github.com/openshift/hypershift/hypershift-operator/controllers/util"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/pointer"
	k8sutilspointer "k8s.io/utils/pointer"
	capiv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// CurrentMachineConfigAnnotationKey is used to fetch current targetConfigVersionHash
	CurrentMachineConfigAnnotationKey = "machineconfiguration.openshift.io/currentConfig"
	// MachineConfigDaemonStateAnnotationKey is used to fetch the state of the daemon on the machine.
	MachineConfigDaemonStateAnnotationKey = "machineconfiguration.openshift.io/state"
	// MachineConfigDaemonStateWorking is set by daemon when it is applying an update.
	MachineConfigDaemonStateWorking = "Working"
	// MachineConfigDaemonStateDone is set by daemon when it is done applying an update.
	MachineConfigDaemonStateDone = "Done"
	// MachineConfigDaemonStateDegraded is set by daemon when an error not caused by a bad MachineConfig
	// is thrown during an update.
	MachineConfigDaemonStateDegraded = "Degraded"
	// MachineConfigDaemonReasonAnnotationKey is set by the daemon when it needs to report a human readable reason for its state. E.g. when state flips to degraded/unreconcilable.
	MachineConfigDaemonReasonAnnotationKey = "machineconfiguration.openshift.io/reason"
)

// reconcileInPlaceUpgrade loops over all Nodes that belong to a NodePool and performs and upgrade if necessary.
func (r *NodePoolReconciler) reconcileInPlaceUpgrade(ctx context.Context, hc *hyperv1.HostedCluster, nodePool *hyperv1.NodePool, machineSet *capiv1.MachineSet, targetConfigHash, targetVersion, targetConfigVersionHash, ignEndpoint string, caCertBytes, tokenBytes []byte) error {
	log := ctrl.LoggerFrom(ctx)

	// If there's no guest cluster yet return early.
	if hc.Status.KubeConfig == nil {
		return nil
	}

	remoteClient, err := newRemoteClient(ctx, r.Client, hc)
	if err != nil {
		return fmt.Errorf("failed to create remote client: %v", err)
	}

	// Watch guest cluster for Nodes.
	if !r.remoteCaches[client.ObjectKeyFromObject(nodePool)] {
		remoteCache, err := newRemoteCache(ctx, r.Client, hc)
		if err != nil {
			return fmt.Errorf("failed to create remote client: %v", err)
		}

		// TODO: cancel the ctx on exit.
		go remoteCache.Start(ctx)
		if !remoteCache.WaitForCacheSync(ctx) {
			return fmt.Errorf("failed waiting for cache for remote cluster to sync: %w", err)
		}

		if err := r.controller.Watch(source.NewKindWithCache(&corev1.Node{}, remoteCache), handler.EnqueueRequestsFromMapFunc(r.nodeToNodePool)); err != nil {
			return fmt.Errorf("error creating watch: %v", err)
		}

		// TODO: index by HC here instead, figure clean up and use locks.
		if r.remoteCaches == nil {
			r.remoteCaches = make(map[client.ObjectKey]bool)
		}
		r.remoteCaches[client.ObjectKeyFromObject(nodePool)] = true
		log.Info("Created remote cache")
	}

	nodes, err := getNodesForNodePool(ctx, r.Client, remoteClient, machineSet)
	if err != nil {
		return err
	}

	if len(nodes) < 1 {
		log.Info("Processing: no nodes available yet")
	}

	// TODO (alberto): Debug, drop.
	for _, node := range nodes {
		log.Info("Processing", "node", node.Name)
	}

	// TODO drop maybe?
	// Since nothing is technically writing the configHash of each node before the first update,
	// We may need to have the nodepool sync stop here if no update is needed
	if nodePool.Annotations[nodePoolAnnotationCurrentConfigVersion] == targetConfigVersionHash {
		log.Info("Inplace upgrade nodepool at desired version. No action required.")
		return nil
	}

	// If all Nodes are atVersion
	if inPlaceUpgradeComplete(nodes, targetConfigVersionHash) {
		// TODO remove update manifests
		// Keeping them around for now for logging purposes
		if nodePool.Status.Version != targetVersion {
			log.Info("Version update complete",
				"previous", nodePool.Status.Version, "new", targetVersion)
			nodePool.Status.Version = targetVersion
		}

		if nodePool.Annotations[nodePoolAnnotationCurrentConfig] != targetConfigHash {
			log.Info("Config update complete",
				"previous", nodePool.Annotations[nodePoolAnnotationCurrentConfig], "new", targetConfigHash)
			nodePool.Annotations[nodePoolAnnotationCurrentConfig] = targetConfigHash
		}
		nodePool.Annotations[nodePoolAnnotationCurrentConfigVersion] = targetConfigVersionHash
		return nil
	}

	// Otherwise:
	// Order Nodes deterministically.
	// Check state: AtVersionConfig, Upgrading, wantVersionConfig.
	// If AtVersionConfig then next Node.
	// If Upgrading then no-op, return.
	// If wantVersionConfig then:
	// Check maxUnavailable/MaxSurge.
	// Drain.
	// Create Namespace/RBAC/ConfigMap/Pod in guest cluster.
	// Mark Node as Upgrading.

	err = createInPlaceUpdateManifests(ctx, remoteClient, targetConfigVersionHash, ignEndpoint, caCertBytes, tokenBytes)
	if err != nil {
		return fmt.Errorf("failed to create update manifests in hosted cluster: %v", err)
	}

	// First check if a node is degraded
	// TODO I think we don't need to set non-degraded conditions since the next sync will overwrite this
	for _, node := range nodes {
		if node.Annotations[MachineConfigDaemonStateAnnotationKey] == MachineConfigDaemonStateDegraded {
			setStatusCondition(&nodePool.Status.Conditions, hyperv1.NodePoolCondition{
				Type:               hyperv1.NodePoolUpdatingVersionConditionType,
				Status:             corev1.ConditionFalse,
				Reason:             hyperv1.NodePoolInplaceUpgradeFailedConditionReason,
				Message:            fmt.Sprintf("Node %s in nodepool degraded: %v", node.Name, node.Annotations[MachineConfigDaemonReasonAnnotationKey]),
				ObservedGeneration: nodePool.Generation,
			})
			return fmt.Errorf("degraded node found! Cannot continue")
		}
	}

	// Then see if any nodes are already updating
	// TODO have logic for maxUnavailable, etc.
	var inProgressNode string
	for _, node := range nodes {
		if node.Annotations[MachineConfigDaemonStateAnnotationKey] == MachineConfigDaemonStateWorking {
			inProgressNode = node.Name
			break
		}
	}
	if inProgressNode != "" {
		log.Info("Existing node in progress ", inProgressNode, " waiting for next sync")
		return nil
	}

	// TODO drain logic should go here
	// Find a node that is not updated
	var updateCandidateNode *corev1.Node
	for _, node := range nodes {
		if node.Annotations[CurrentMachineConfigAnnotationKey] != targetConfigVersionHash {
			updateCandidateNode = node
			break
		}
	}
	if updateCandidateNode == nil {
		return fmt.Errorf("no node available to be upgraded")
	}

	err = setUpdatePodForNode(ctx, remoteClient, updateCandidateNode, targetConfigVersionHash)
	if err != nil {
		return fmt.Errorf("failed to create update pod in hosted cluster for node %s: %v", updateCandidateNode, err)
	}
	return nil
}

func setNodeWorking(ctx context.Context, remoteClient client.Client, node *corev1.Node) error {
	// TODO set node status
	// This probably needs a design, namely, who writes what conditions (setworking/done/degraded vs currentconfig annotation)
	// To ensure we never deadlock ourself waiting for a completed update, etc.
	annos := map[string]string{
		MachineConfigDaemonStateAnnotationKey: MachineConfigDaemonStateWorking,
	}

	newNode := node.DeepCopy()
	if newNode.Annotations == nil {
		newNode.Annotations = map[string]string{}
	}

	for k, v := range annos {
		newNode.Annotations[k] = v
	}

	if _, err := controllerutil.CreateOrPatch(ctx, remoteClient, newNode, nil); err != nil {
		return fmt.Errorf("failed to set node working for %s: %v", node.Name, err)
	}
	return nil
}

func setUpdatePodForNode(ctx context.Context, remoteClient client.Client, node *corev1.Node, targetConfigVersionHash string) error {
	// TODO un-hardcode a lot of this
	pod := &corev1.Pod{}
	pod.APIVersion = corev1.SchemeGroupVersion.String()
	pod.Kind = "Pod"
	// Making this unique for now for debugging purposes
	pod.Name = "machine-config-daemon-hypershift-" + node.Name + "-" + targetConfigVersionHash
	pod.Namespace = "hypershift-mco"
	pod.Spec.Containers = []corev1.Container{
		{
			Name:  "machine-config-daemon",
			Image: "quay.io/jerzhang/hypershiftdaemon:latest",
			Command: []string{
				"/usr/bin/machine-config-daemon",
			},
			Args: []string{
				"start",
				"--node-name=" + node.Name,
				"--root-mount=/rootfs",
				"--kubeconfig=/var/lib/kubelet/kubeconfig",
				"--desired-configmap=/etc/machine-config-daemon-desired-config",
			},
			Env: []corev1.EnvVar{
				{
					Name: "POD_NAME",
					ValueFrom: &corev1.EnvVarSource{
						FieldRef: &corev1.ObjectFieldSelector{
							FieldPath: "metadata.name",
						},
					},
				},
			},
			TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
			SecurityContext: &corev1.SecurityContext{
				Privileged: pointer.BoolPtr(true),
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "rootfs",
					MountPath: "/rootfs",
				},
				{
					Name:      "desired-config-mount",
					MountPath: "/rootfs/etc/machine-config-daemon-desired-config",
				},
			},
		},
	}
	pod.Spec.HostNetwork = true
	pod.Spec.HostPID = true
	pod.Spec.ServiceAccountName = "machine-config-daemon-hypershift"
	pod.Spec.Tolerations = []corev1.Toleration{
		{
			Operator: corev1.TolerationOpExists,
		},
	}
	pod.Spec.NodeSelector = map[string]string{
		"kubernetes.io/hostname": node.Name,
	}
	pod.Spec.Volumes = []corev1.Volume{
		{
			Name: "rootfs",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/",
				},
			},
		},
		{
			Name: "desired-config-mount",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: "desired-config",
					},
				},
			},
		},
	}
	pod.Spec.RestartPolicy = corev1.RestartPolicyOnFailure

	if _, err := controllerutil.CreateOrPatch(ctx, remoteClient, pod, nil); err != nil {
		return fmt.Errorf("failed to reconcile update pod: %v", err)
	}

	if err := setNodeWorking(ctx, remoteClient, node); err != nil {
		return fmt.Errorf("failed to set node working annotation: %v", err)
	}
	return nil
}

func createInPlaceUpdateManifests(ctx context.Context, remoteClient client.Client, targetConfigVersionHash, ignEndpoint string, caCertBytes, tokenBytes []byte) error {
	// TODO properly create the objects
	// TODO use updated CreateOrUpdate func
	// TODO un-hardcode objects
	log := ctrl.LoggerFrom(ctx)

	namespace := "hypershift-mco"
	ns := &corev1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: corev1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}

	if _, err := controllerutil.CreateOrPatch(ctx, remoteClient, ns, nil); err != nil {
		return fmt.Errorf("failed to reconcile update namespace: %v", err)
	}

	saName := "machine-config-daemon-hypershift"
	sa := &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ServiceAccount",
			APIVersion: corev1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      saName,
		},
	}
	if _, err := controllerutil.CreateOrPatch(ctx, remoteClient, sa, nil); err != nil {
		return fmt.Errorf("failed to reconcile update serviceaccount: %v", err)
	}

	roleName := "machine-config-daemon-hypershift"
	role := &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ClusterRole",
			APIVersion: rbacv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      roleName,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get", "list", "watch", "patch", "update"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods/eviction"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list"},
			},
			{
				APIGroups: []string{"extensions"},
				Resources: []string{"daemonsets"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"daemonsets"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"get"},
			},
		},
	}

	if _, err := controllerutil.CreateOrPatch(ctx, remoteClient, role, nil); err != nil {
		return fmt.Errorf("failed to reconcile update clusterrole: %v", err)
	}

	binding := &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ClusterRoleBinding",
			APIVersion: rbacv1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      roleName,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     roleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: namespace,
			},
		},
	}

	if _, err := controllerutil.CreateOrPatch(ctx, remoteClient, binding, nil); err != nil {
		return fmt.Errorf("failed to reconcile update clusterrolebinding: %v", err)
	}

	// fetch desired config off our ign endpoint and then stuff into configmap
	// TODO reconsider this workflow
	ignURL := fmt.Sprintf("https://%s/ignition", ignEndpoint)
	req, err := http.NewRequest("GET", ignURL, nil)
	if err != nil {
		return errors.Wrapf(err, "Failed to construct request")
	}
	req.Header.Add("Accept", "application/vnd.coreos.ignition+json;version=3.2.0, */*;q=0.1")
	encodedToken := base64.StdEncoding.EncodeToString(tokenBytes)
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", encodedToken))
	log.Info("Checking Authorization token",
		"TEST", fmt.Sprintf("Bearer %s", encodedToken))

	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCertBytes)
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: caCertPool,
			},
		},
		Timeout: 5 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.Wrapf(err, "Failed to get desired config from MCS endpoint")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("request to the machine config server returned a bad status")
	}
	defer resp.Body.Close()

	respData, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// TODO the respData here needs to be parsed for a better workflow. For now just stuff it in
	configmap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "desired-config",
			Namespace: namespace,
		},
		Data: map[string]string{
			"config": string(respData),
			"hash":   targetConfigVersionHash,
		},
	}
	if _, err := controllerutil.CreateOrPatch(ctx, remoteClient, configmap, nil); err != nil {
		return fmt.Errorf("failed to reconcile update configmap: %v", err)
	}

	return nil
}

func (r *NodePoolReconciler) nodeToNodePool(o client.Object) []reconcile.Request {
	node, ok := o.(*corev1.Node)
	if !ok {
		panic(fmt.Sprintf("Expected a Node but got a %T", o))
	}

	machineName, ok := node.GetAnnotations()[capiv1.MachineAnnotation]
	if !ok {
		return nil
	}

	// Match by namespace when the node has the annotation.
	machineNamespace, ok := node.GetAnnotations()[capiv1.ClusterNamespaceAnnotation]
	if !ok {
		return nil
	}

	// Match by nodeName and status.nodeRef.name.
	machine := &capiv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: machineNamespace,
			Name:      machineName,
		},
	}
	if err := r.Client.Get(context.TODO(), client.ObjectKeyFromObject(machine), machine); err != nil {
		return nil
	}

	machineOwner := metav1.GetControllerOf(machine)
	if machineOwner.Kind != "MachineSet" {
		return nil
	}

	machineSet := &capiv1.MachineSet{ObjectMeta: metav1.ObjectMeta{
		Name:      machineOwner.Name,
		Namespace: machineNamespace,
	}}
	if err := r.Client.Get(context.TODO(), client.ObjectKeyFromObject(machineSet), machineSet); err != nil {
		return nil
	}
	nodePoolName := machineSet.GetAnnotations()[nodePoolAnnotation]
	if nodePoolName == "" {
		return nil
	}

	return []reconcile.Request{
		{NamespacedName: hyperutil.ParseNamespacedName(nodePoolName)},
	}
}

func getNodesForNodePool(ctx context.Context, c client.Reader, remoteClient client.Client, machineSet *capiv1.MachineSet) ([]*corev1.Node, error) {
	selectorMap, err := metav1.LabelSelectorAsMap(&machineSet.Spec.Selector)
	if err != nil {
		return nil, fmt.Errorf("failed to convert MachineSet %q label selector to a map: %v", machineSet.Name, err)
	}

	// Get all Machines linked to this MachineSet.
	allMachines := &capiv1.MachineList{}
	if err = c.List(ctx,
		allMachines,
		client.InNamespace(machineSet.Namespace),
		client.MatchingLabels(selectorMap),
	); err != nil {
		return nil, fmt.Errorf("failed to list machines: %v", err)
	}

	var machineSetOwnedMachines []capiv1.Machine
	for i, machine := range allMachines.Items {
		if metav1.GetControllerOf(&machine) != nil && metav1.IsControlledBy(&machine, machineSet) {
			machineSetOwnedMachines = append(machineSetOwnedMachines, allMachines.Items[i])
		}
	}

	var nodes []*corev1.Node
	for _, machine := range machineSetOwnedMachines {
		if machine.Status.NodeRef != nil {
			node := &corev1.Node{}
			if err := remoteClient.Get(ctx, client.ObjectKey{Name: machine.Status.NodeRef.Name}, node); err != nil {
				return nil, fmt.Errorf("error getting node: %v", err)
			}
			nodes = append(nodes, node)
		}
	}

	return nodes, nil
}

// TODO (alberto): implement.
func inPlaceUpgradeComplete(nodes []*corev1.Node, targetVersionConfig string) bool {
	for _, node := range nodes {
		// This is written by the MCD oneshot pod
		if node.Annotations[CurrentMachineConfigAnnotationKey] != targetVersionConfig {
			return false
		}
	}

	// TODO remove pods/configmap/rbac/namespace etc.
	return true
}

func targetRESTConfig(ctx context.Context, c client.Reader, hc *hyperv1.HostedCluster) (*restclient.Config, error) {
	log := ctrl.LoggerFrom(ctx)

	if hc.Status.KubeConfig == nil {
		log.Info("No kubeConfig available yet")
		return nil, nil
	}

	// Resolve the kubeconfig secret for CAPI.
	// TODO (alberto): Provider adhoc kubeconfig.
	kubeconfigSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: hc.Namespace,
			Name:      fmt.Sprintf("%s-admin-kubeconfig", hc.Name),
		},
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(kubeconfigSecret), kubeconfigSecret); err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig secret %q: %w", kubeconfigSecret.Name, err)
	}

	// TODO (alberto): not hardcode key here.
	kubeConfig, ok := kubeconfigSecret.Data["kubeconfig"]
	if !ok {
		return nil, fmt.Errorf("failed to get kubeconfig from secret %q", kubeconfigSecret.Name)
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create REST config kubeconfig from secret %q", kubeconfigSecret.Name)
	}

	restConfig.UserAgent = "nodepool-controller"
	// restConfig.Timeout = defaultClientTimeout

	return restConfig, nil
}

// NewRemoteClient returns a Client for interacting with a remote Cluster using the given scheme for encoding and decoding objects.
func newRemoteClient(ctx context.Context, c client.Client, hc *hyperv1.HostedCluster) (client.Client, error) {
	restConfig, err := targetRESTConfig(ctx, c, hc)
	if err != nil {
		return nil, err
	}

	remoteClient, err := client.New(restConfig, client.Options{Scheme: c.Scheme()})
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %v", err)
	}

	return remoteClient, nil
}

// NewRemoteClient returns a Client for interacting with a remote Cluster using the given scheme for encoding and decoding objects.
func newRemoteCache(ctx context.Context, c client.Client, hc *hyperv1.HostedCluster) (cache.Cache, error) {
	restConfig, err := targetRESTConfig(ctx, c, hc)
	if err != nil {
		return nil, err
	}

	remoteCache, err := cache.New(restConfig, cache.Options{Scheme: c.Scheme()})
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %v", err)
	}

	return remoteCache, nil
}

func (r *NodePoolReconciler) reconcileMachineSet(ctx context.Context,
	machineSet *capiv1.MachineSet,
	hc *hyperv1.HostedCluster,
	nodePool *hyperv1.NodePool,
	userDataSecret *corev1.Secret,
	machineTemplateCR client.Object,
	CAPIClusterName string,
	targetVersion,
	targetConfigHash, targetConfigVersionHash, machineTemplateSpecJSON string) error {

	log := ctrl.LoggerFrom(ctx)
	// Set annotations and labels
	if machineSet.GetAnnotations() == nil {
		machineSet.Annotations = map[string]string{}
	}
	machineSet.Annotations[nodePoolAnnotation] = client.ObjectKeyFromObject(nodePool).String()
	if machineSet.GetLabels() == nil {
		machineSet.Labels = map[string]string{}
	}
	machineSet.Labels[capiv1.ClusterLabelName] = CAPIClusterName

	resourcesName := generateName(CAPIClusterName, nodePool.Spec.ClusterName, nodePool.GetName())
	machineSet.Spec.MinReadySeconds = int32(0)

	gvk, err := apiutil.GVKForObject(machineTemplateCR, api.Scheme)
	if err != nil {
		return err
	}

	// Set selector and template
	machineSet.Spec.ClusterName = CAPIClusterName
	if machineSet.Spec.Selector.MatchLabels == nil {
		machineSet.Spec.Selector.MatchLabels = map[string]string{}
	}
	machineSet.Spec.Selector.MatchLabels[resourcesName] = resourcesName
	machineSet.Spec.Template = capiv1.MachineTemplateSpec{
		ObjectMeta: capiv1.ObjectMeta{
			Labels: map[string]string{
				resourcesName:           resourcesName,
				capiv1.ClusterLabelName: CAPIClusterName,
			},
			Annotations: map[string]string{
				// TODO (alberto): Use conditions to signal an in progress rolling upgrade
				// similar to what we do with nodePoolAnnotationCurrentConfig
				nodePoolAnnotationPlatformMachineTemplate: machineTemplateSpecJSON, // This will trigger a deployment rolling upgrade when its value changes.
			},
		},

		Spec: capiv1.MachineSpec{
			ClusterName: CAPIClusterName,
			Bootstrap: capiv1.Bootstrap{
				// Keep current user data for later check.
				DataSecretName: machineSet.Spec.Template.Spec.Bootstrap.DataSecretName,
			},
			InfrastructureRef: corev1.ObjectReference{
				Kind:       gvk.Kind,
				APIVersion: gvk.GroupVersion().String(),
				Namespace:  machineTemplateCR.GetNamespace(),
				Name:       machineTemplateCR.GetName(),
			},
			// Keep current version for later check.
			Version:          machineSet.Spec.Template.Spec.Version,
			NodeDrainTimeout: nodePool.Spec.NodeDrainTimeout,
		},
	}

	// Propagate version and userData Secret to the machineDeployment.
	if userDataSecret.Name != k8sutilspointer.StringPtrDerefOr(machineSet.Spec.Template.Spec.Bootstrap.DataSecretName, "") {
		log.Info("New user data Secret has been generated",
			"current", machineSet.Spec.Template.Spec.Bootstrap.DataSecretName,
			"target", userDataSecret.Name)

		// TODO (alberto): possibly compare with NodePool here instead so we don't rely on impl details to drive decisions.
		if targetVersion != k8sutilspointer.StringPtrDerefOr(machineSet.Spec.Template.Spec.Version, "") {
			log.Info("Starting version update: Propagating new version to the MachineSet",
				"releaseImage", nodePool.Spec.Release.Image, "target", targetVersion)
		}

		if targetConfigHash != nodePool.Annotations[nodePoolAnnotationCurrentConfig] {
			log.Info("Starting config update: Propagating new config to the MachineDeployment",
				"current", nodePool.Annotations[nodePoolAnnotationCurrentConfig], "target", targetConfigHash)
		}
		machineSet.Spec.Template.Spec.Version = &targetVersion
		machineSet.Spec.Template.Spec.Bootstrap.DataSecretName = k8sutilspointer.StringPtr(userDataSecret.Name)

		// We return early here during a version/config update to persist the resource with new user data Secret,
		// so in the next reconciling loop we get a new machineSet.Generation
		// and we can do a legit MachineDeploymentComplete/MachineDeployment.Status.ObservedGeneration check.
		// Before persisting, if the NodePool is brand new we want to make sure the replica number is set so the machineDeployment controller
		// does not panic.
		if machineSet.Spec.Replicas == nil {
			machineSet.Spec.Replicas = k8sutilspointer.Int32Ptr(k8sutilspointer.Int32PtrDerefOr(nodePool.Spec.NodeCount, 0))
		}
		return nil
	}

	setMachineSetReplicas(nodePool, machineSet)

	// Bubble up AvailableReplicas and Ready condition from MachineDeployment.
	nodePool.Status.NodeCount = machineSet.Status.AvailableReplicas
	for _, c := range machineSet.Status.Conditions {
		// This condition should aggregate and summarise readiness from underlying MachineSets and Machines
		// https://github.com/kubernetes-sigs/cluster-api/issues/3486.
		if c.Type == capiv1.ReadyCondition {
			// this is so api server does not complain
			// invalid value: \"\": status.conditions.reason in body should be at least 1 chars long"
			reason := hyperv1.NodePoolAsExpectedConditionReason
			if c.Reason != "" {
				reason = c.Reason
			}

			setStatusCondition(&nodePool.Status.Conditions, hyperv1.NodePoolCondition{
				Type:               hyperv1.NodePoolReadyConditionType,
				Status:             c.Status,
				ObservedGeneration: nodePool.Generation,
				Message:            c.Message,
				Reason:             reason,
			})
			break
		}
	}

	return nil
}

// setMachineSetReplicas sets wanted replicas:
// If autoscaling is enabled we reconcile min/max annotations and leave replicas untouched.
func setMachineSetReplicas(nodePool *hyperv1.NodePool, machineSet *capiv1.MachineSet) {
	if machineSet.Annotations == nil {
		machineSet.Annotations = make(map[string]string)
	}

	if isAutoscalingEnabled(nodePool) {
		if k8sutilspointer.Int32PtrDerefOr(machineSet.Spec.Replicas, 0) == 0 {
			// if autoscaling is enabled and the machineDeployment does not exist yet or it has 0 replicas
			// we set it to 1 replica as the autoscaler does not support scaling from zero yet.
			machineSet.Spec.Replicas = k8sutilspointer.Int32Ptr(int32(1))
		}
		machineSet.Annotations[autoscalerMaxAnnotation] = strconv.Itoa(int(nodePool.Spec.AutoScaling.Max))
		machineSet.Annotations[autoscalerMinAnnotation] = strconv.Itoa(int(nodePool.Spec.AutoScaling.Min))
	}

	// If autoscaling is NOT enabled we reset min/max annotations and reconcile replicas.
	if !isAutoscalingEnabled(nodePool) {
		machineSet.Annotations[autoscalerMaxAnnotation] = "0"
		machineSet.Annotations[autoscalerMinAnnotation] = "0"
		machineSet.Spec.Replicas = k8sutilspointer.Int32Ptr(k8sutilspointer.Int32PtrDerefOr(nodePool.Spec.NodeCount, 0))
	}
}
