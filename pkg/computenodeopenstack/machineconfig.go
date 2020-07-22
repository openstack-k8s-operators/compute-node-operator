package computenodeopenstack

import (
	"context"

	mcfgv1 "github.com/openshift/machine-config-operator/pkg/apis/machineconfiguration.openshift.io/v1"
	computenodev1alpha1 "github.com/openstack-k8s-operators/compute-node-operator/pkg/apis/computenode/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetMachineConfigWithLabel - Returns list of machineconfigpools labeled with labelSelector
func GetMachineConfigWithLabel(c client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack, labelSelector map[string]string) (*mcfgv1.MachineConfigList, error) {
	machineConfigs := &mcfgv1.MachineConfigList{}
	listOpts := []client.ListOption{
		client.MatchingLabels(labelSelector),
	}
	if err := c.List(context.Background(), machineConfigs, listOpts...); err != nil {
		return nil, err
	}

	return machineConfigs, nil
}

// GetMachineConfigPoolWithLabel - Returns list of machineconfigpools labeled with labelSelector
func GetMachineConfigPoolWithLabel(c client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack, labelSelector map[string]string) (*mcfgv1.MachineConfigPoolList, error) {
	machineConfigPools := &mcfgv1.MachineConfigPoolList{}
	listOpts := []client.ListOption{
		client.MatchingLabels(labelSelector),
	}
	if err := c.List(context.Background(), machineConfigPools, listOpts...); err != nil {
		return nil, err
	}

	return machineConfigPools, nil
}
