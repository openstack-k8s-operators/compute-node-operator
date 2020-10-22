package computenodeopenstack

import (
	"context"

	performancev1alpha1 "github.com/openshift-kni/performance-addon-operators/pkg/apis/performance/v1alpha1"
	computenodev1alpha1 "github.com/openstack-k8s-operators/compute-node-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetPerformanceProfilesWithLabel - Returns list of performanceprofiles labeled with labelSelector
func GetPerformanceProfilesWithLabel(c client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack, labelSelector map[string]string) (*performancev1alpha1.PerformanceProfileList, error) {
	performanceProfiles := &performancev1alpha1.PerformanceProfileList{}
	listOpts := []client.ListOption{
		client.MatchingLabels(labelSelector),
	}
	if err := c.List(context.Background(), performanceProfiles, listOpts...); err != nil {
		return nil, err
	}

	return performanceProfiles, nil
}
