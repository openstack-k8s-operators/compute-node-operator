package util

import (
	"context"

	machinev1beta1 "github.com/openshift/cluster-api/pkg/apis/machine/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetMachineSetsForMachine - get the machineset for a machine object
func GetMachineSetsForMachine(c client.Client, m *machinev1beta1.Machine) []*machinev1beta1.MachineSet {
	if len(m.Labels) == 0 {
		klog.Warningf("No machine sets found for Machine %v because it has no labels", m.Name)
		return nil
	}

	msList := &machinev1beta1.MachineSetList{}
	err := c.List(context.Background(), msList, client.InNamespace(m.Namespace))
	if err != nil {
		klog.Errorf("Failed to list machine sets, %v", err)
		return nil
	}

	var mss []*machinev1beta1.MachineSet
	for idx := range msList.Items {
		ms := &msList.Items[idx]
		hasLabels := hasMatchingLabels(ms, m)
		if hasLabels {
			mss = append(mss, ms)
		}
	}

	return mss
}

func hasMatchingLabels(machineSet *machinev1beta1.MachineSet, machine *machinev1beta1.Machine) bool {
	selector, err := metav1.LabelSelectorAsSelector(&machineSet.Spec.Selector)
	if err != nil {
		klog.Warningf("unable to convert selector: %v", err)
		return false
	}

	// If a deployment with a nil or empty selector creeps in, it should match nothing, not everything.
	if selector.Empty() {
		klog.V(2).Infof("%v machineset has empty selector", machineSet.Name)
		return false
	}

	if !selector.Matches(labels.Set(machine.Labels)) {
		klog.V(4).Infof("%v machine has mismatch labels", machine.Name)
		return false
	}

	return true
}
