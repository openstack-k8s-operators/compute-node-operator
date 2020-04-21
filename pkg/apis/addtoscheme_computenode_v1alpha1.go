package apis

import (
	"github.com/openstack-k8s-operators/compute-node-operator/pkg/apis/computenode/v1alpha1"
)

func init() {
	// Register the types with the Scheme so the components can map objects to GroupVersionKinds and back
	AddToSchemes = append(AddToSchemes, v1alpha1.SchemeBuilder.AddToScheme)
}
