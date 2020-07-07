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
	K8sServiceIP string `json:"k8sServiceIP"`
	// Internal Cluster API IP (app-int)
	APIIntIP string `json:"apiIntIP"`
	// Number of workers
	Workers int32 `json:"workers,omitempty"`
	// Cores Pinning
	CorePinning string `json:"corePinning,omitempty"`
	// Infra DaemonSets needed
	InfraDaemonSets []InfraDaemonSet `json:"infraDaemonSets,omitempty"`
	// Nodes to delete upon scale down
	NodesToDelete []NodeToDelete `json:"nodesToDelete,omitempty"`
	// openstackclient configmap which holds information to connect to OpenStack API
	OpenStackClientConfigMap string `json:"openStackClientConfigMap"`
	// user secrets used to connect to OpenStack API via openstackclient
	OpenStackClientAdminSecret string `json:"openStackClientAdminSecret"`
	// Node draining configuration options
	Drain DrainParam `json:"drain"`
	// Compute/Nova configuration
	Compute NovaCompute `json:"compute,omitempty"`
	// Network/Neutron configuration
	Network NeutronNetwork `json:"network,omitempty"`
}

// InfraDaemonSet defines the daemon set required
type InfraDaemonSet struct {
	// Namespace
	Namespace string `json:"namespace"`
	// Name
	Name string `json:"name"`
}

// DrainParam defines global draining specific parameters
type DrainParam struct {
	// Automatic draining (live migrate off instances) of the node, global switch, which can be overwritten on per Node base using NodeToDelete struct. Default: false
	Enabled bool `json:"enabled,omitempty"`
	// Image used for drain pod which performs compute removal, this is usually
	// an image which has the openstackclient and osc-placement packages installed.
	DrainPodImage string `json:"drainPodImage"`
}

// NovaCompute defines nova configuration parameters
type NovaCompute struct {
	// CPU Dedicated Set (pinning)
	NovaComputeCPUDedicatedSet string `json:"novaComputeCPUDedicatedSet,omitempty"`
	// CPU Shared Set
	NovaComputeCPUSharedSet string `json:"novaComputeCPUSharedSet,omitempty"`
	// sshd migration port
	SshdPort int32 `json:"sshdPort,omitempty"`
	// Nova configMap containing the common config
	CommonConfigMap string `json:"commonConfigMap,omitempty"`
	// Nova secret containing the needed passwords
	OspSecrets string `json:"ospSecrets,omitempty"`
}

// NeutronNetwork defines neutron configuration parameters
type NeutronNetwork struct {
	Nic				 string `json:"nic"`
	BridgeMappings   string      `json:"bridgeMappings,omitempty"`
	MechanishDrivers string      `json:"mechanismDrivers,omitempty"`
	ServicePlugings  string      `json:"servicePlugins,omitempty"`
	Sriov            SriovConfig `json:"sriov,omitempty"`
}

// SriovConfig defines SRIOV config parameters, such as nic information.
type SriovConfig struct {
	DevName string `json:"devName"`
}

// NodeToDelete defines the name of the node to delete and if automatic drain is needed
type NodeToDelete struct {
	// Node Name
	Name string `json:"name"`
	// Automatic draining of the node
	Drain bool `json:"drain,omitempty"`
}

// Node defines the status of the associated nodes
type Node struct {
	// Node name
	Name string `json:"name"`
	// Node status
	Status string `json:"status"`
}

// DisabledNode list the already disabled nodes in OSP
type DisabledNode struct {
	// Node Name
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
	// Nodes information
	Nodes []Node `json:"nodes,omitempty"`
	// Nodes to delete upon scale down
	NodesToDelete []NodeToDelete `json:"nodesToDelete,omitempty"`
	// DisabledNodes imformation
	DisabledNodes []DisabledNode `json:"disabledNode,omitempty"`
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
