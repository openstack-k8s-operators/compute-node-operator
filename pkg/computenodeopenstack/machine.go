package computenodeopenstack

import (
	"context"
	"strings"

	computenodev1alpha1 "github.com/openstack-k8s-operators/compute-node-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	machinev1beta1 "github.com/openshift/cluster-api/pkg/apis/machine/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IsMachineBeingDeleted - Returns true if machine matching nodeName has DeletionTimestamp set
func IsMachineBeingDeleted(c client.Client, kclient kubernetes.Interface, instance *computenodev1alpha1.ComputeNodeOpenStack, nodeName string) (bool, error) {
	isDeleted := false

	node, err := GetNodeWithName(kclient, nodeName)
	if err != nil {
		return isDeleted, err
	}

	machine, err := GetMachineFromNodeName(c, node)
	// if Machine not found it got deleted
	if err != nil && errors.IsNotFound(err) {
		isDeleted = true
	} else if err != nil {
		return isDeleted, err
	}

	deletionTimestamp := machine.DeletionTimestamp
	if deletionTimestamp != nil {
		isDeleted = true
	}

	return isDeleted, nil
}

// GetMachineFromNodeName - Returns machine object for node
func GetMachineFromNodeName(c client.Client, node *corev1.Node) (*machinev1beta1.Machine, error) {
	machineAnnotation := node.ObjectMeta.Annotations["machine.openshift.io/machine"]

	machine := &machinev1beta1.Machine{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: strings.Split(machineAnnotation, "/")[1], Namespace: "openshift-machine-api"}, machine)
	if err != nil {
		return nil, err
	}
	return machine, nil
}

// GetNodeWithName - Returns node object for name
func GetNodeWithName(kclient kubernetes.Interface, nodeName string) (*corev1.Node, error) {

	node, err := kclient.CoreV1().Nodes().Get(nodeName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return node, nil
}
