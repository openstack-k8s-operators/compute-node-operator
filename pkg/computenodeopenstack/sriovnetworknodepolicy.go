package computenodeopenstack

import (
	"context"

	sriovnetworkv1 "github.com/openshift/sriov-network-operator/pkg/apis/sriovnetwork/v1"
	computenodev1alpha1 "github.com/openstack-k8s-operators/compute-node-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetSriovNetworkNodePoliciesWithLabel - Returns list of sriovnetworknodepolicies labeled with labelSelector
func GetSriovNetworkNodePoliciesWithLabel(c client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack, labelSelector map[string]string, namespace string) (*sriovnetworkv1.SriovNetworkNodePolicyList, error) {
	sriovNetworkNodePolicies := &sriovnetworkv1.SriovNetworkNodePolicyList{}
	listOpts := []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels(labelSelector),
	}
	if err := c.List(context.Background(), sriovNetworkNodePolicies, listOpts...); err != nil {
		return nil, err
	}

	return sriovNetworkNodePolicies, nil
}
