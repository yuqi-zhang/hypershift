// Code generated by go-swagger; DO NOT EDIT.

package p_cloud_v_p_n_policies

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	"fmt"
	"io"

	"github.com/go-openapi/runtime"
	"github.com/go-openapi/strfmt"

	"github.com/IBM-Cloud/power-go-client/power/models"
)

// PcloudIkepoliciesGetReader is a Reader for the PcloudIkepoliciesGet structure.
type PcloudIkepoliciesGetReader struct {
	formats strfmt.Registry
}

// ReadResponse reads a server response into the received o.
func (o *PcloudIkepoliciesGetReader) ReadResponse(response runtime.ClientResponse, consumer runtime.Consumer) (interface{}, error) {
	switch response.Code() {
	case 200:
		result := NewPcloudIkepoliciesGetOK()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return result, nil
	case 400:
		result := NewPcloudIkepoliciesGetBadRequest()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return nil, result
	case 401:
		result := NewPcloudIkepoliciesGetUnauthorized()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return nil, result
	case 403:
		result := NewPcloudIkepoliciesGetForbidden()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return nil, result
	case 404:
		result := NewPcloudIkepoliciesGetNotFound()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return nil, result
	case 422:
		result := NewPcloudIkepoliciesGetUnprocessableEntity()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return nil, result
	case 500:
		result := NewPcloudIkepoliciesGetInternalServerError()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return nil, result
	default:
		return nil, runtime.NewAPIError("response status code does not match any response statuses defined for this endpoint in the swagger spec", response, response.Code())
	}
}

// NewPcloudIkepoliciesGetOK creates a PcloudIkepoliciesGetOK with default headers values
func NewPcloudIkepoliciesGetOK() *PcloudIkepoliciesGetOK {
	return &PcloudIkepoliciesGetOK{}
}

/* PcloudIkepoliciesGetOK describes a response with status code 200, with default header values.

OK
*/
type PcloudIkepoliciesGetOK struct {
	Payload *models.IKEPolicy
}

func (o *PcloudIkepoliciesGetOK) Error() string {
	return fmt.Sprintf("[GET /pcloud/v1/cloud-instances/{cloud_instance_id}/vpn/ike-policies/{ike_policy_id}][%d] pcloudIkepoliciesGetOK  %+v", 200, o.Payload)
}
func (o *PcloudIkepoliciesGetOK) GetPayload() *models.IKEPolicy {
	return o.Payload
}

func (o *PcloudIkepoliciesGetOK) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.IKEPolicy)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewPcloudIkepoliciesGetBadRequest creates a PcloudIkepoliciesGetBadRequest with default headers values
func NewPcloudIkepoliciesGetBadRequest() *PcloudIkepoliciesGetBadRequest {
	return &PcloudIkepoliciesGetBadRequest{}
}

/* PcloudIkepoliciesGetBadRequest describes a response with status code 400, with default header values.

Bad Request
*/
type PcloudIkepoliciesGetBadRequest struct {
	Payload *models.Error
}

func (o *PcloudIkepoliciesGetBadRequest) Error() string {
	return fmt.Sprintf("[GET /pcloud/v1/cloud-instances/{cloud_instance_id}/vpn/ike-policies/{ike_policy_id}][%d] pcloudIkepoliciesGetBadRequest  %+v", 400, o.Payload)
}
func (o *PcloudIkepoliciesGetBadRequest) GetPayload() *models.Error {
	return o.Payload
}

func (o *PcloudIkepoliciesGetBadRequest) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.Error)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewPcloudIkepoliciesGetUnauthorized creates a PcloudIkepoliciesGetUnauthorized with default headers values
func NewPcloudIkepoliciesGetUnauthorized() *PcloudIkepoliciesGetUnauthorized {
	return &PcloudIkepoliciesGetUnauthorized{}
}

/* PcloudIkepoliciesGetUnauthorized describes a response with status code 401, with default header values.

Unauthorized
*/
type PcloudIkepoliciesGetUnauthorized struct {
	Payload *models.Error
}

func (o *PcloudIkepoliciesGetUnauthorized) Error() string {
	return fmt.Sprintf("[GET /pcloud/v1/cloud-instances/{cloud_instance_id}/vpn/ike-policies/{ike_policy_id}][%d] pcloudIkepoliciesGetUnauthorized  %+v", 401, o.Payload)
}
func (o *PcloudIkepoliciesGetUnauthorized) GetPayload() *models.Error {
	return o.Payload
}

func (o *PcloudIkepoliciesGetUnauthorized) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.Error)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewPcloudIkepoliciesGetForbidden creates a PcloudIkepoliciesGetForbidden with default headers values
func NewPcloudIkepoliciesGetForbidden() *PcloudIkepoliciesGetForbidden {
	return &PcloudIkepoliciesGetForbidden{}
}

/* PcloudIkepoliciesGetForbidden describes a response with status code 403, with default header values.

Forbidden
*/
type PcloudIkepoliciesGetForbidden struct {
	Payload *models.Error
}

func (o *PcloudIkepoliciesGetForbidden) Error() string {
	return fmt.Sprintf("[GET /pcloud/v1/cloud-instances/{cloud_instance_id}/vpn/ike-policies/{ike_policy_id}][%d] pcloudIkepoliciesGetForbidden  %+v", 403, o.Payload)
}
func (o *PcloudIkepoliciesGetForbidden) GetPayload() *models.Error {
	return o.Payload
}

func (o *PcloudIkepoliciesGetForbidden) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.Error)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewPcloudIkepoliciesGetNotFound creates a PcloudIkepoliciesGetNotFound with default headers values
func NewPcloudIkepoliciesGetNotFound() *PcloudIkepoliciesGetNotFound {
	return &PcloudIkepoliciesGetNotFound{}
}

/* PcloudIkepoliciesGetNotFound describes a response with status code 404, with default header values.

Not Found
*/
type PcloudIkepoliciesGetNotFound struct {
	Payload *models.Error
}

func (o *PcloudIkepoliciesGetNotFound) Error() string {
	return fmt.Sprintf("[GET /pcloud/v1/cloud-instances/{cloud_instance_id}/vpn/ike-policies/{ike_policy_id}][%d] pcloudIkepoliciesGetNotFound  %+v", 404, o.Payload)
}
func (o *PcloudIkepoliciesGetNotFound) GetPayload() *models.Error {
	return o.Payload
}

func (o *PcloudIkepoliciesGetNotFound) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.Error)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewPcloudIkepoliciesGetUnprocessableEntity creates a PcloudIkepoliciesGetUnprocessableEntity with default headers values
func NewPcloudIkepoliciesGetUnprocessableEntity() *PcloudIkepoliciesGetUnprocessableEntity {
	return &PcloudIkepoliciesGetUnprocessableEntity{}
}

/* PcloudIkepoliciesGetUnprocessableEntity describes a response with status code 422, with default header values.

Unprocessable Entity
*/
type PcloudIkepoliciesGetUnprocessableEntity struct {
	Payload *models.Error
}

func (o *PcloudIkepoliciesGetUnprocessableEntity) Error() string {
	return fmt.Sprintf("[GET /pcloud/v1/cloud-instances/{cloud_instance_id}/vpn/ike-policies/{ike_policy_id}][%d] pcloudIkepoliciesGetUnprocessableEntity  %+v", 422, o.Payload)
}
func (o *PcloudIkepoliciesGetUnprocessableEntity) GetPayload() *models.Error {
	return o.Payload
}

func (o *PcloudIkepoliciesGetUnprocessableEntity) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.Error)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewPcloudIkepoliciesGetInternalServerError creates a PcloudIkepoliciesGetInternalServerError with default headers values
func NewPcloudIkepoliciesGetInternalServerError() *PcloudIkepoliciesGetInternalServerError {
	return &PcloudIkepoliciesGetInternalServerError{}
}

/* PcloudIkepoliciesGetInternalServerError describes a response with status code 500, with default header values.

Internal Server Error
*/
type PcloudIkepoliciesGetInternalServerError struct {
	Payload *models.Error
}

func (o *PcloudIkepoliciesGetInternalServerError) Error() string {
	return fmt.Sprintf("[GET /pcloud/v1/cloud-instances/{cloud_instance_id}/vpn/ike-policies/{ike_policy_id}][%d] pcloudIkepoliciesGetInternalServerError  %+v", 500, o.Payload)
}
func (o *PcloudIkepoliciesGetInternalServerError) GetPayload() *models.Error {
	return o.Payload
}

func (o *PcloudIkepoliciesGetInternalServerError) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.Error)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}