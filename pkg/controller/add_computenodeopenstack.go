package controller

import (
	"github.com/openstack-k8s-operators/compute-node-operator/pkg/controller/computenodeopenstack"
)

func init() {
	// AddToManagerFuncs is a list of functions to create controllers and add them to a manager.
	AddToManagerFuncs = append(AddToManagerFuncs, computenodeopenstack.Add)
}
