package nodepool

import (
	"context"
	"fmt"
	"strconv"

	api "github.com/openshift/hypershift/api"
	hyperv1 "github.com/openshift/hypershift/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/pointer"
	k8sutilspointer "k8s.io/utils/pointer"
	capiv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// MachineConfigDaemonStateAnnotationKey is used to fetch the state of the daemon on the machine.
	MachineConfigDaemonStateAnnotationKey = "machineconfiguration.openshift.io/state"
	// MachineConfigDaemonStateWorking is set by daemon when it is applying an update.
	MachineConfigDaemonStateWorking = "Working"
	// MachineConfigDaemonStateDone is set by daemon when it is done applying an update.
	MachineConfigDaemonStateDone = "Done"
	// MachineConfigDaemonStateDegraded is set by daemon when an error not caused by a bad MachineConfig
	// is thrown during an update.
	MachineConfigDaemonStateDegraded = "Degraded"
)

// reconcileInPlaceUpgrade loops over all Nodes that belong to a NodePool and performs and upgrade if necessary.
func (r *NodePoolReconciler) reconcileInPlaceUpgrade(ctx context.Context, hc *hyperv1.HostedCluster, nodePool *hyperv1.NodePool, machineSet *capiv1.MachineSet, targetConfigHash, targetVersion, targetConfigVersionHash string) error {
	log := ctrl.LoggerFrom(ctx)

	// TODO (alberto): add remote watcher for Nodes.
	remoteClient, err := newRemoteClient(ctx, r.Client, hc)
	if err != nil {
		return fmt.Errorf("failed to create remote client: %v, err")
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

	// If all Nodes are atVersion
	if inPlaceUpgradeComplete(nodes, targetConfigVersionHash) {
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
	// Create Namespace/RBAC/ConfigMap/Pod in guest cluster.
	// Mark Node as Upgrading.

	// Testing: start the upgrade if we are not at version
	if nodePool.Status.Version == targetVersion && nodePool.Annotations[nodePoolAnnotationCurrentConfig] == targetConfigHash {
		log.Info("In inplace upgrade path, no update required.")
		return nil
	}

	ready, err := createInPlaceUpdateManifests(ctx, remoteClient)
	if err != nil {
		return fmt.Errorf("failed to create update manifests in hosted cluster: %v", err)
	}
	if !ready {
		// wait for next sync
		log.Info("Manifests completed successfully, waiting for configmap.")
		return nil
	}

	// TODO: need to pre-set each node's annotation to see if they are atversion/updating/updated
	// For now just pick 1 if nobody is in progress
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

	// TODO hack to test workflow
	err = setUpdatePodForNode(ctx, remoteClient, nodes[0].Name)
	if err != nil {
		return fmt.Errorf("failed to create update pod in hosted cluster for node %s: %v", nodes[0].Name, err)
	}

	err = setNodeWorking(ctx, remoteClient, nodes[0].Name)

	return nil
}

func setNodeWorking(ctx context.Context, remoteClient client.Client, nodeName string) error {
	// TODO set node status
	return nil
}

func setUpdatePodForNode(ctx context.Context, remoteClient client.Client, nodeName string) error {
	pod := &corev1.Pod{}
	pod.APIVersion = corev1.SchemeGroupVersion.String()
	pod.Kind = "Pod"
	pod.Name = "machine-config-daemon-hypershift"
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
				"--node-name=" + nodeName,
				"--root-mount=/rootfs",
				"--hypershift=desired-config",
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
	pod.Spec.Volumes = []corev1.Volume{
		{
			Name: "rootfs",
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: "/",
				},
			},
		},
	}
	pod.Spec.RestartPolicy = corev1.RestartPolicyNever

	if _, err := controllerutil.CreateOrPatch(ctx, remoteClient, pod, nil); err != nil {
		return fmt.Errorf("failed to reconcile update pod: %v", err)
	}
	return nil
}

func createInPlaceUpdateManifests(ctx context.Context, remoteClient client.Client) (bool, error) {
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
		return false, fmt.Errorf("failed to reconcile update namespace: %v", err)
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
		return false, fmt.Errorf("failed to reconcile update serviceaccount: %v", err)
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
		return false, fmt.Errorf("failed to reconcile update clusterrole: %v", err)
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
		return false, fmt.Errorf("failed to reconcile update clusterrolebinding: %v", err)
	}

	// TODO fetch configuration from ignition-server controller. For now just do nothing if the expected configmap is not created.
	configmap := &corev1.ConfigMap{}
	if err := remoteClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "desired-config"}, configmap); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to reconcile update configmap: %v", err)
	}
	return true, nil
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
	return false
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
			log.Info("Starting version update: Propagating new version to the MachineDeployment",
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
