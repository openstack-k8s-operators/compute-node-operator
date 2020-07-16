package computenodeopenstack

import (
	"context"

	"github.com/openshift/cluster-api/pkg/apis/machine/v1beta1"
	computenodev1alpha1 "github.com/openstack-k8s-operators/compute-node-operator/pkg/apis/computenode/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetMachineSetsWithLabel - Returns list of machinesets labeled with labelSelector
func GetMachineSetsWithLabel(c client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack, labelSelector map[string]string, namespace string) (*v1beta1.MachineSetList, error) {
	machineSets := &v1beta1.MachineSetList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels(labelSelector),
	}
	if err := c.List(context.Background(), machineSets, listOpts...); err != nil {
		return nil, err
	}

	return machineSets, nil
}
