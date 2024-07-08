package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type EventType string

const (
	Observe EventType = "Observe"
	Create  EventType = "Create"
	Update  EventType = "Update"
	Delete  EventType = "Delete"
)

type ObjectRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`
}

func (o *ObjectRef) String() string {
	return fmt.Sprintf("%s.%s as %s@%s", o.APIVersion, o.Kind, o.Name, o.Namespace)
}

type event struct {
	id        string
	eventType EventType
	objectRef ObjectRef
}

// An ExternalClient manages the lifecycle of an external resource.
// None of the calls here should be blocking. All of the calls should be
// idempotent. For example, Create call should not return AlreadyExists error
// if it's called again with the same parameters or Delete call should not
// return error if there is an ongoing deletion or resource does not exist.
type ExternalClient interface {
	Observe(ctx context.Context, mg *unstructured.Unstructured) (bool, error)
	Create(ctx context.Context, mg *unstructured.Unstructured) error
	Update(ctx context.Context, mg *unstructured.Unstructured) error
	Delete(ctx context.Context, mg *unstructured.Unstructured) error
}

// An ExternalObservation is the result of an observation of an external resource.
type ExternalObservation struct {
	// ResourceExists must be true if a corresponding external resource exists
	// for the managed resource.
	ResourceExists bool

	// ResourceUpToDate should be true if the corresponding external resource
	// appears to be up-to-date - i.e. updating the external resource to match
	// the desired state of the managed resource would be a no-op.
	ResourceUpToDate bool
}
