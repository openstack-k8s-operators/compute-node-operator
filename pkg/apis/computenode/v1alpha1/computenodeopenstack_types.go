package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ComputeNodeOpenStackSpec defines the desired state of ComputeNodeOpenStack
type ComputeNodeOpenStackSpec struct {
	// Name of the worker role created for OSP computes
	RoleName string `json:"roleName"`
	// Cluster name
	ClusterName string `json:"clusterName"`
	// Base Worker MachineSet Name
	BaseWorkerMachineSetName string `json:"baseWorkerMachineSetName"`
	// Kubernetes service cluster IP
	K8sServiceIP string `json:"k8sServiceIp"`
	// Internal Cluster API IP (app-int)
	APIIntIP string `json:"apiIntIp"`
	// Number of workers
	Workers int32 `json:"workers,omitempty"`
	// Cores Pinning
	CorePinning string `json:"corePinning,omitempty"`
	// Infra DaemonSets needed
	InfraDaemonSets []InfraDaemonSet `json:"infraDaemonSets,omitempty"`
}

// InfraDaemonSet defines the daemon set required
type InfraDaemonSet struct {
	// Namespace
	Namespace string `json:"namespace"`
	// Name
	Name string `json:"name"`
}

// ComputeNodeOpenStackStatus defines the observed state of ComputeNodeOpenStack
type ComputeNodeOpenStackStatus struct {
	// Important: Run "operator-sdk generate k8s" to regenerate code after modifying this file
	// Add custom validation using kubebuilder tags: https://book-v1.book.kubebuilder.io/beyond_basics/generating_crd.html
	// Number of requested workers
	Workers int32 `json:"workers"`
	// Number of ready workers
	ReadyWorkers int32 `json:"readyWorkers,omitempty"`
	// Infra DaemonSets created
	InfraDaemonSets []InfraDaemonSet `json:"infraDaemonSets,omitempty"`
	// Applied Spec
	SpecMDS string `json:"specMDS"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ComputeNodeOpenStack is the Schema for the computenodeopenstacks API
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=computenodeopenstacks,scope=Namespaced
type ComputeNodeOpenStack struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ComputeNodeOpenStackSpec   `json:"spec,omitempty"`
	Status ComputeNodeOpenStackStatus `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ComputeNodeOpenStackList contains a list of ComputeNodeOpenStack
type ComputeNodeOpenStackList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ComputeNodeOpenStack `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ComputeNodeOpenStack{}, &ComputeNodeOpenStackList{})
}
