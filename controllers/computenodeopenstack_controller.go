/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/common/log"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	machinev1beta1 "github.com/openshift/cluster-api/pkg/apis/machine/v1beta1"
	sriovnetworkv1 "github.com/openshift/sriov-network-operator/pkg/apis/sriovnetwork/v1"
	computenodev1alpha1 "github.com/openstack-k8s-operators/compute-node-operator/api/v1alpha1"
	bindatautil "github.com/openstack-k8s-operators/compute-node-operator/pkg/bindata_util"
	computenodeopenstack "github.com/openstack-k8s-operators/compute-node-operator/pkg/computenodeopenstack"
	util "github.com/openstack-k8s-operators/compute-node-operator/pkg/util"
	libcommonutil "github.com/openstack-k8s-operators/lib-common/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	manifestEnvVar              = "OPERATOR_BINDATA"
	ownerUIDLabelSelector       = "computenodeopenstacks.compute-node.openstack.org/uid"
	ownerNameSpaceLabelSelector = "computenodeopenstacks.compute-node.openstack.org/namespace"
	ownerNameLabelSelector      = "computenodeopenstacks.compute-node.openstack.org/name"
)

// ManifestPath is the path to the manifest templates
var ManifestPath = "./bindata"

// ResyncPeriod - The periodic resync interval.
// We will re-run the reconciliation logic, even if the configuration hasn't changed.
var ResyncPeriod = 10 * time.Minute

// ComputeNodeOpenStackReconciler reconciles a ComputeNodeOpenStack object
type ComputeNodeOpenStackReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	Log     logr.Logger
	Scheme  *runtime.Scheme
}

// +kubebuilder:rbac:groups=core,resources=pods;services;services/finalizers;endpoints;persistentvolumeclaims;events;configmaps;secrets,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;update;watch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=apps,resources=deployments;replicasets,verbs=get
// +kubebuilder:rbac:groups=compute-node.openstack.org,resources=computenodeopenstacks,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=compute-node.openstack.org,resources=computenodeopenstacks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=compute-node.openstack.org,resources=deployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=neutron.openstack.org,resources=ovncontrollers;ovsnodeosps,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=nova.openstack.org,resources=novacomputes;virtlogds;libvirtds;iscsids;novamigrationtargets,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=machine.openshift.io;machineconfiguration.openshift.io;sriovnetwork.openshift.io,resources="*",verbs="*"
// +kubebuilder:rbac:groups=core,namespace=openstack,resources=pods,verbs=create;delete;get;list;patch;update;watch
// +kubebuilder:rbac:groups=security.openshift.io,namespace=openstack,resources=securitycontextconstraints,resourceNames=hostnetwork,verbs=use

// Reconcile reconcile computenodeopenstack API requests
func (r *ComputeNodeOpenStackReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	_ = context.Background()
	_ = r.Log.WithValues("computenodeopenstack", req.NamespacedName)

	// Fetch the ComputeNodeOpenStack instance
	instance := &computenodev1alpha1.ComputeNodeOpenStack{}

	err := r.Client.Get(context.TODO(), req.NamespacedName, instance)
	if err != nil {
		// Error reading the object - requeue the request.
		// ignore not found errors, since they can't be fixed by an immediate
		// requeue, and we can get them on deleted requests which we now
		// handle using finalizer.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	finalizerName := "compute-node-operator-" + instance.Spec.RoleName
	// if deletion timestamp is set on the instance object, the CR got deleted
	if instance.ObjectMeta.DeletionTimestamp.IsZero() {
		// if it is a new instance, add the finalizer
		if !controllerutil.ContainsFinalizer(instance, finalizerName) {
			controllerutil.AddFinalizer(instance, finalizerName)
			err = r.Client.Update(context.TODO(), instance)
			if err != nil {
				return ctrl.Result{}, err
			}
			r.Log.Info(fmt.Sprintf("Finalizer %s added to CR %s", finalizerName, instance.Name))
		}
	} else {
		// 1. check if finalizer is there
		// Reconcile if finalizer got already removed
		if !controllerutil.ContainsFinalizer(instance, finalizerName) {
			return ctrl.Result{}, nil
		}

		// 2. cleanup resources created by operator
		// a. - delete blocker pods for the compute worker nodes
		//    - run cleanup jobs to remove compute node service entries in OSP control plane
		r.Log.Info(fmt.Sprintf("CR %s delete, running cleanup", instance.Name))
		err := deleteAllBlockerPodFinalizer(r, instance)
		if err != nil && errors.IsAlreadyExists(err) {
			// AlreadyExists error will be ignored as we want to wait for the event
			// of the drain jobs to not spam the operator log with too many not needed
			// reconcile runs.
			return ctrl.Result{}, nil
		}
		if err != nil {
			return ctrl.Result{}, err
		}

		// b. Delete objects in non openstack namespace which have the owner reference label
		//    - remove infra daemonsets with owner reference label
		//    - cleanup machineset and secret objects in openshift-machine-api namespace
		//    - machineconfigpool
		//    - machineconfig
		err = deleteOwnerRefLabeledObjects(r, instance)
		if err != nil && !errors.IsNotFound(err) {
			// ignore not found errors if the object is already gone
			return ctrl.Result{}, err
		}
		// 3. as last step remove the finalizer on the operator CR to finish delete
		controllerutil.RemoveFinalizer(instance, finalizerName)
		err = r.Client.Update(context.TODO(), instance)
		if err != nil {
			return ctrl.Result{}, err
		}
		r.Log.Info(fmt.Sprintf("CR %s deleted", instance.Name))
		return ctrl.Result{}, nil
	}

	// ScriptsConfigMap
	err = ensureComputeNodeOpenStackScriptsConfigMap(r, instance, strings.ToLower(instance.Kind)+"-scripts")
	if err != nil {
		return ctrl.Result{}, err
	}

	// openStackClientAdminSecret and openStackClientConfigMap are expected to exist
	// mschuppert - note:   the format of the secret/configmap might change when the OSP controller
	//                      operators who in the end should populate this information are done. So
	//                      likely changes are expected.
	openStackClientAdmin, openStackClient, err := getOpenStackClientInformation(r, instance)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Available nodes in the role's machineset
	var nodeCount int32

	specMDS, err := util.CalculateHash(instance.Spec)
	if err != nil {
		return ctrl.Result{}, err
	}
	if reflect.DeepEqual(specMDS, instance.Status.SpecMDS) {
		nodeCount, err = ensureMachineSetSync(r.Client, instance)
		if err != nil {
			return ctrl.Result{}, err
		}
		err = r.updateNodesStatus(instance, openStackClientAdmin, openStackClient)
		if err != nil {
			return ctrl.Result{}, err
		}
	} else if !reflect.DeepEqual(instance.Spec.NodesToDelete, instance.Status.NodesToDelete) || instance.Spec.Workers < instance.Status.Workers {
		// Check if nodes to delete information has changed
		err := updateMachineDeletionSelection(r.Client, instance)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	// Need to reapply the spec
	// Fill all defaults explicitly
	data, err := getRenderData(context.TODO(), r.Client, instance)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Needed for SRIOV considerations in templates, because you do not want to attempt creating
	// SriovNetworkNodePolicy resources if there are no nodes available in this role's machineset.
	// Otherwise the SRIOV operator will throw an error when trying to apply any SriovNetworkNodePolicy
	// that targets the nodes with the role (as it will find no nodes available, which it considers
	// an error)
	data.Data["AvailableNodeCount"] = nodeCount

	// Handle SRIOV removal (if any -- add/update handled by "Worker" template logic below)
	err = ensureSriovRemovalSync(r.Client, instance)
	if err != nil {
		return ctrl.Result{}, err
	}

	// if run from image OPERATOR_BINDATA env has the bindata
	manifestPath, found := os.LookupEnv(manifestEnvVar)
	if found {
		ManifestPath = manifestPath
	}

	// Generate the Worker objects
	objs := []*uns.Unstructured{}
	manifests, err := bindatautil.RenderDir(filepath.Join(ManifestPath, "worker-osp"), &data)
	if err != nil {
		log.Error(err, "Failed to render worker manifests : %v")
		return ctrl.Result{}, err
	}
	objs = append(objs, manifests...)

	// Generate the Compute objects
	manifests, err = bindatautil.RenderDir(filepath.Join(ManifestPath, "nova"), &data)
	if err != nil {
		log.Error(err, "Failed to render nova manifests : %v")
		return ctrl.Result{}, err
	}
	objs = append(objs, manifests...)

	// Generate the Neutron objects
	manifests, err = bindatautil.RenderDir(filepath.Join(ManifestPath, "neutron"), &data)
	if err != nil {
		r.Log.Error(err, "Failed to render neutron manifests : %v")
		return ctrl.Result{}, err
	}
	objs = append(objs, manifests...)

	// Apply the objects to the cluster
	oref := metav1.NewControllerRef(instance, instance.GroupVersionKind())
	labelSelector := map[string]string{
		ownerUIDLabelSelector:       string(instance.UID),
		ownerNameSpaceLabelSelector: instance.Namespace,
		ownerNameLabelSelector:      instance.Name,
	}
	for _, obj := range objs {
		// Set owner reference on objects in the same namespace as the operator
		if obj.GetNamespace() == instance.Namespace {
			obj.SetOwnerReferences([]metav1.OwnerReference{*oref})
		}
		// merge owner ref label into labels on the objects
		obj.SetLabels(labels.Merge(obj.GetLabels(), labelSelector))
		objs = append(objs, obj)

		// Open question: should an error here indicate we will never retry?
		if err := bindatautil.ApplyObject(context.TODO(), r.Client, obj); err != nil {
			r.Log.Error(err, "Failed to apply objects")
			return ctrl.Result{}, err
		}
	}

	/* Only create the new ones, deleting the old ones not needed anymore
	   // create node-exporter daemonset (to have monitoring information)
	   // create machine-config-daemon daemonset(to allow reconfigurations)
	   // create multus daemonset (to set the node to ready)
	*/
	if err := ensureInfraDaemonsets(r.Kclient, instance); err != nil {
		r.Log.Error(err, "Failed to create the infra daemon sets")
		return ctrl.Result{}, err
	}
	r.Log.Info("Created InfraDaemonSets")

	// Update Status
	instance.Status.Workers = instance.Spec.Workers
	instance.Status.InfraDaemonSets = instance.Spec.InfraDaemonSets
	instance.Status.NodesToDelete = instance.Spec.NodesToDelete
	instance.Status.SpecMDS = specMDS
	err = r.Client.Status().Update(context.TODO(), instance)
	if err != nil {
		r.Log.Error(err, "Failed to update CR status %v")
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: ResyncPeriod}, nil
}

// SetupWithManager -
func (r *ComputeNodeOpenStackReconciler) SetupWithManager(mgr ctrl.Manager) error {

	// watch for MachineSet with compute-node-operator CR owner ref
	ospMachineSetFn := handler.ToRequestsFunc(func(o handler.MapObject) []reconcile.Request {
		cc := mgr.GetClient()
		result := []reconcile.Request{}
		ms := &machinev1beta1.MachineSet{}
		key := client.ObjectKey{Namespace: o.Meta.GetNamespace(), Name: o.Meta.GetName()}
		err := cc.Get(context.Background(), key, ms)
		if err != nil && !errors.IsNotFound(err) {
			log.Error(err, "Unable to retrieve MachineSet %v")
			return nil
		}

		label := ms.ObjectMeta.GetLabels()
		// verify object has ownerUIDLabelSelector
		if uid, ok := label[ownerUIDLabelSelector]; ok {
			log.Info(fmt.Sprintf("MachineSet object %s marked with OSP owner ref: %s", o.Meta.GetName(), uid))
			// return namespace and Name of CR
			name := client.ObjectKey{
				Namespace: label[ownerNameSpaceLabelSelector],
				Name:      label[ownerNameLabelSelector],
			}
			result = append(result, reconcile.Request{NamespacedName: name})
		}
		if len(result) > 0 {
			return result
		}
		return nil
	})

	// watch for SriovNetworkNodePolicy with compute-node-operator CR owner ref
	ospSriovNetworkNodePolicyFn := handler.ToRequestsFunc(func(o handler.MapObject) []reconcile.Request {
		cc := mgr.GetClient()
		result := []reconcile.Request{}
		snnp := &sriovnetworkv1.SriovNetworkNodePolicy{}
		key := client.ObjectKey{Namespace: o.Meta.GetNamespace(), Name: o.Meta.GetName()}
		err := cc.Get(context.Background(), key, snnp)
		if err != nil && !errors.IsNotFound(err) {
			log.Error(err, "Unable to retrieve SriovNetworkNodePolicy %v")
			return nil
		}

		label := snnp.ObjectMeta.GetLabels()
		// verify object has ownerUIDLabelSelector
		if uid, ok := label[ownerUIDLabelSelector]; ok {
			log.Info(fmt.Sprintf("SriovNetworkNodePolicy object %s marked with OSP owner ref: %s", o.Meta.GetName(), uid))
			// return namespace and Name of CR
			name := client.ObjectKey{
				Namespace: label[ownerNameSpaceLabelSelector],
				Name:      label[ownerNameLabelSelector],
			}
			result = append(result, reconcile.Request{NamespacedName: name})
		}
		if len(result) > 0 {
			return result
		}
		return nil
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&computenodev1alpha1.ComputeNodeOpenStack{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.ConfigMap{}).
		Watches(&source.Kind{Type: &machinev1beta1.MachineSet{}},
			&handler.EnqueueRequestsFromMapFunc{
				ToRequests: ospMachineSetFn,
			}).
		Watches(&source.Kind{Type: &sriovnetworkv1.SriovNetworkNodePolicy{}},
			&handler.EnqueueRequestsFromMapFunc{
				ToRequests: ospSriovNetworkNodePolicyFn,
			}).
		Complete(r)
}

func getRenderData(ctx context.Context, client client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack) (bindatautil.RenderData, error) {
	data := bindatautil.MakeRenderData()
	// default to cell1 if not specifiec in CR
	cell := "cell1"
	if instance.Spec.Cell != "" {
		cell = instance.Spec.Cell
	}

	data.Data["ClusterName"] = instance.Spec.ClusterName
	data.Data["WorkerOspRole"] = instance.Spec.RoleName
	data.Data["Workers"] = instance.Spec.Workers
	data.Data["Dedicated"] = false
	if instance.Spec.Dedicated {
		data.Data["Dedicated"] = instance.Spec.Dedicated
	}
	data.Data["NetworkGateway"] = false
	if instance.Spec.NetworkGateway {
		data.Data["NetworkGateway"] = instance.Spec.NetworkGateway
	}

	data.Data["Isolcpus"] = false
	data.Data["SshdPort"] = 2022
	data.Data["NovaComputeCPUDedicatedSet"] = ""
	data.Data["NovaComputeCPUSharedSet"] = ""
	data.Data["OspSecrets"] = "osp-secrets"
	if instance.Spec.Compute.NovaComputeCPUDedicatedSet != "" {
		data.Data["Isolcpus"] = true
		data.Data["NovaComputeCPUDedicatedSet"] = instance.Spec.Compute.NovaComputeCPUDedicatedSet
	}
	if instance.Spec.Compute.NovaComputeCPUSharedSet != "" {
		data.Data["NovaComputeCPUSharedSet"] = instance.Spec.Compute.NovaComputeCPUSharedSet
	}
	if instance.Spec.Compute.SshdPort != 0 {
		data.Data["SshdPort"] = instance.Spec.Compute.SshdPort
	}
	if instance.Spec.Compute.NovaSecret != "" {
		data.Data["NovaSecret"] = instance.Spec.Compute.NovaSecret
	} else {
		data.Data["NovaSecret"] = "nova-secret"
	}
	if instance.Spec.Compute.NeutronSecret != "" {
		data.Data["NeutronSecret"] = instance.Spec.Compute.NeutronSecret
	} else {
		data.Data["NeutronSecret"] = "neutron-secret"
	}
	if instance.Spec.Compute.PlacementSecret != "" {
		data.Data["PlacementSecret"] = instance.Spec.Compute.PlacementSecret
	} else {
		data.Data["PlacementSecret"] = "placement-secret"
	}
	if instance.Spec.Compute.TransportURLSecret != "" {
		data.Data["TransportURLSecret"] = instance.Spec.Compute.TransportURLSecret
	} else {
		data.Data["TransportURLSecret"] = fmt.Sprintf("nova-%s-transport-url", cell)
	}

	data.Data["Nic"] = "enp2s0"
	if instance.Spec.Network.Nic != "" {
		data.Data["Nic"] = instance.Spec.Network.Nic
	}
	data.Data["BridgeMappings"] = "datacentre:br-ex"
	if instance.Spec.Network.BridgeMappings != "" {
		data.Data["BridgeMappings"] = instance.Spec.Network.BridgeMappings
	}

	// disable selinux if set
	// TODO use performance tuning operator for this when implemented for cpu pinning, ...
	data.Data["SelinuxDisabled"] = false
	if instance.Spec.SelinuxDisabled {
		data.Data["SelinuxDisabled"] = true
		log.Info(fmt.Sprintf("SELINUX will be DISABLED for worker role: %s!!", data.Data["WorkerOspRole"]))
	}
	// get it from openshift-machine-api secrets (assumes worker-user-data)
	userData := &corev1.Secret{}
	err := client.Get(context.TODO(), types.NamespacedName{Name: "worker-user-data", Namespace: "openshift-machine-api"}, userData)
	if err != nil {
		return data, err
	}
	modifiedUserData := strings.Replace(string(userData.Data["userData"]), "worker", instance.Spec.RoleName, 1)
	encodedModifiedUserData := base64.StdEncoding.EncodeToString([]byte(modifiedUserData))
	data.Data["WorkerOspUserData"] = encodedModifiedUserData

	// get it from openshift-machine-api machineset
	workerMachineSet := &machinev1beta1.MachineSet{}
	err = client.Get(context.TODO(), types.NamespacedName{Name: instance.Spec.BaseWorkerMachineSetName, Namespace: "openshift-machine-api"}, workerMachineSet)
	if err != nil {
		return data, err
	}
	providerSpec := workerMachineSet.Spec.Template.Spec.ProviderSpec.Value.Raw
	providerData := make(map[string]map[string]interface{})
	err = json.Unmarshal(providerSpec, &providerData)
	if err != nil {
		return data, err
	}
	data.Data["RhcosImageUrl"] = providerData["image"]["url"]

	// Set SriovConfig data
	data.Data["Sriov"] = instance.Spec.Network.Sriov

	return data, nil
}

func ensureInfraDaemonsets(client kubernetes.Interface, instance *computenodev1alpha1.ComputeNodeOpenStack) error {
	// Create/Update the required ones
	for _, dsInfo := range instance.Spec.InfraDaemonSets {
		originDaemonSet := &appsv1.DaemonSet{}
		originDaemonSet, err := client.AppsV1().DaemonSets(dsInfo.Namespace).Get(context.TODO(), dsInfo.Name, metav1.GetOptions{})
		if err != nil && errors.IsNotFound(err) {
			log.Error(err, "Failed to find the daemon set", dsInfo.Name, dsInfo.Namespace)
			return err
		} else if err != nil {
			return err
		} else {
			ospDaemonSet := &appsv1.DaemonSet{}
			ospDaemonSetName := dsInfo.Name + "-" + instance.Spec.RoleName
			ospDaemonSet, err = client.AppsV1().DaemonSets(dsInfo.Namespace).Get(context.TODO(), ospDaemonSetName, metav1.GetOptions{})
			if err != nil && errors.IsNotFound(err) {
				// Creating a new Daemonset ospDaemonSetName
				ds := newDaemonSet(instance, originDaemonSet, ospDaemonSetName, dsInfo.Namespace)
				_, err := client.AppsV1().DaemonSets(dsInfo.Namespace).Create(context.TODO(), ds, metav1.CreateOptions{})
				if err != nil {
					log.Error(err, "Error creating Daemonset", ospDaemonSetName)
					return err
				}
			} else if err != nil {
				log.Error(err, "Error getting Daemonset:", ospDaemonSetName)
				return err
			} else {
				// Updating the Daemonset
				ds := newDaemonSet(instance, originDaemonSet, ospDaemonSetName, dsInfo.Namespace)
				// Merge the desired object with what actually exists
				if !reflect.DeepEqual(ospDaemonSet.Spec, ds.Spec) {
					_, err := client.AppsV1().DaemonSets(dsInfo.Namespace).Update(context.TODO(), ds, metav1.UpdateOptions{})
					if err != nil {
						log.Error(err, "could not update object", ospDaemonSetName)
						return err
					}
				}
			}
		}
	}
	// Remove the extra ones
	for _, statusDsInfo := range instance.Status.InfraDaemonSets {
		existing := false
		for _, specDsInfo := range instance.Spec.InfraDaemonSets {
			if reflect.DeepEqual(statusDsInfo, specDsInfo) {
				existing = true
				break
			}
		}
		if !existing {
			ospDaemonSetName := statusDsInfo.Name + "-" + instance.Spec.RoleName
			err := client.AppsV1().DaemonSets(statusDsInfo.Namespace).Delete(context.TODO(), ospDaemonSetName, metav1.DeleteOptions{})
			if err != nil && errors.IsNotFound(err) {
				// Already deleted
				continue
			} else if err != nil {
				return err
			}
		}
	}
	return nil
}

func newDaemonSet(instance *computenodev1alpha1.ComputeNodeOpenStack, ds *appsv1.DaemonSet, name string, namespace string) *appsv1.DaemonSet {
	daemonSet := appsv1.DaemonSet{
		TypeMeta: metav1.TypeMeta{
			Kind:       "DaemonSet",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				ownerUIDLabelSelector:       string(instance.UID),
				ownerNameSpaceLabelSelector: instance.Namespace,
				ownerNameLabelSelector:      instance.Name,
			},
		},
		Spec: ds.Spec,
	}

	// Update template name
	daemonSet.Spec.Template.ObjectMeta.Name = name

	// Add toleration
	tolerationSpec := corev1.Toleration{
		Operator: "Equal",
		Effect:   "NoSchedule",
		Key:      "dedicated",
		Value:    instance.Spec.RoleName,
	}
	daemonSet.Spec.Template.Spec.Tolerations = append(daemonSet.Spec.Template.Spec.Tolerations, tolerationSpec)

	// Change nodeSelector
	nodeSelector := "node-role.kubernetes.io/" + instance.Spec.RoleName
	daemonSet.Spec.Template.Spec.NodeSelector = map[string]string{nodeSelector: ""}

	return &daemonSet
}

func (r *ComputeNodeOpenStackReconciler) updateNodesStatus(instance *computenodev1alpha1.ComputeNodeOpenStack, openstackClientAdmin *corev1.Secret, openstackClient *corev1.ConfigMap) error {

	// With the operator watching multiple namespaces provided as a list the c client
	// returns each entry multiple times. The kclient returns only a single entry.
	workerLabel := "node-role.kubernetes.io/" + instance.Spec.RoleName
	nodeList, err := r.Kclient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{LabelSelector: workerLabel})
	if err != nil {
		return err
	}

	var readyWorkers int32 = 0
	nodesStatus := []computenodev1alpha1.Node{}
	for _, node := range nodeList.Items {
		log.Info(fmt.Sprintf("Update node status for node: %s", node.Name))
		nodeStatus := getNodeStatus(r.Client, &node)
		previousNodeStatus := getPreviousNodeStatus(node.Name, instance)

		switch nodeStatus {
		case "NotReady":
			log.Info(fmt.Sprintf("Node status is NotReady: %s", node.Name))
			if previousNodeStatus == "None" {
				// Create blocker pod
				err = createBlockerPod(r.Client, node.Name, instance)
				if err != nil {
					return err
				}
			}
			// add node to status
			newNode := computenodev1alpha1.Node{Name: node.Name, Status: nodeStatus}
			nodesStatus = append(nodesStatus, newNode)
		case "Ready":
			log.Info(fmt.Sprintf("Node status is Ready: %s", node.Name))
			if previousNodeStatus == "None" {
				// Create blocker pod
				err = createBlockerPod(r.Client, node.Name, instance)
				if err != nil {
					return err
				}
			}
			// add node to status
			newNode := computenodev1alpha1.Node{Name: node.Name, Status: nodeStatus}
			nodesStatus = append(nodesStatus, newNode)
			readyWorkers++
		case "SchedulingDisabled":
			log.Info(fmt.Sprintf("Node status is SchedulingDisabled: %s", node.Name))
			// If previous status is None, blocker pod was not created, nothing to do
			if previousNodeStatus == "None" {
				break
			}

			// It might happen that the node switches to SchedulingDisabled while there
			// is no machine deletion, e.g. when there is an issue with the machineconfig.
			// Therefore check if the machine object has the DeletionTimestamp, or is
			// already deleted, otherwise the node should not get drained
			machineDeleted, err := computenodeopenstack.IsMachineBeingDeleted(r.Client, r.Kclient, instance, node.Name)
			if err != nil {
				return err
			} else if !machineDeleted {
				log.Info(fmt.Sprintf("Node %s marked as unschedulable, but machine object not flagged for deletion, skip draining!", node.Name))
				break
			}

			// If blocker pod finalizer was already deleted, there is nothing extra to do either
			finalizerDeleted, err := isBlockerPodFinalizerDeleted(r.Client, node.Name, instance)
			if err != nil {
				return err
			} else if finalizerDeleted {
				break
			}

			/* Steps to delete the drain the node
			   1. Predraining: disable nova service, (optional) migrate VMs, wait until there is no VMs
			   2. Postdraining: taint the node, remove nova-compute from nova services and placement
			   3. Remove cleanup jobs, blocker pod finalizer, and update nodesToDelete status information
			*/
			// 1. NodePreDrain
			nodePreDrained, err := ensureNodePreDrain(r.Client, node.Name, instance, openstackClientAdmin, openstackClient)
			// AlreadyExists error will be ignored as we want to wait for the job event to
			// not spam the operator log with too many not needed reconcile runs.
			if err != nil && !errors.IsAlreadyExists(err) {
				return err
			}
			if !nodePreDrained {
				// add node to status
				newNode := computenodev1alpha1.Node{Name: node.Name, Status: nodeStatus}
				nodesStatus = append(nodesStatus, newNode)
				break
			}
			log.Info(fmt.Sprintf("Pre draining job completed: %t", nodePreDrained))

			// 2. NodePostDrain
			// a) taint the node to have OSP pods stopped
			err = addToBeRemovedTaint(r.Kclient, node)
			if err != nil {
				return err
			}
			// b) run post drain job
			nodePostDrained, err := ensureNodePostDrain(r.Client, node.Name, instance, openstackClientAdmin, openstackClient)
			if err != nil && errors.IsAlreadyExists(err) {
				// AlreadyExists error will be ignored as we want to wait for the job event to
				// not spam the operator log with too many not needed reconcile runs.
				return nil
			}
			if err != nil {
				return err
			}
			if !nodePostDrained {
				// add node to status
				newNode := computenodev1alpha1.Node{Name: node.Name, Status: nodeStatus}
				nodesStatus = append(nodesStatus, newNode)
				break
			}
			log.Info(fmt.Sprintf("Post draining job completed: %t", nodePostDrained))

			// 3. Cleanup
			log.Info(fmt.Sprintf("Deleting draining jobs for node: %s", node.Name))
			err = deleteJobWithName(r.Client, r.Kclient, instance, node.Name+"-drain-job-post")
			if err != nil && !errors.IsNotFound(err) {
				return err
			}
			err = deleteJobWithName(r.Client, r.Kclient, instance, node.Name+"-drain-job-pre")
			if err != nil && !errors.IsNotFound(err) {
				return err
			}

			log.Info(fmt.Sprintf("Deleting blocker pod finalizer for node: %s", node.Name))
			// delete blocker pod to proceed with node removal from OCP
			err = deleteBlockerPodFinalizer(r.Client, node.Name, instance)
			if err != nil {
				return err
			}

			log.Info(fmt.Sprintf("Updating nodeToDelete status"))
			for i, nodeToDelete := range instance.Spec.NodesToDelete {
				if nodeToDelete.Name == node.Name {
					if len(instance.Spec.NodesToDelete) == 1 {
						instance.Spec.NodesToDelete = []computenodev1alpha1.NodeToDelete{}
					} else {
						instance.Spec.NodesToDelete = removeNode(instance.Spec.NodesToDelete, i)
					}
					err = r.Client.Update(context.TODO(), instance)
					if err != nil {
						return err
					}
					break
				}
			}
		}
	}

	if !reflect.DeepEqual(instance.Status.Nodes, nodesStatus) {
		instance.Status.ReadyWorkers = readyWorkers
		instance.Status.Nodes = nodesStatus
		err = r.Client.Status().Update(context.TODO(), instance)
		if err != nil {
			return err
		}
	}
	return nil
}

func removeNode(nodes []computenodev1alpha1.NodeToDelete, i int) []computenodev1alpha1.NodeToDelete {
	nodes[i] = nodes[len(nodes)-1]
	return nodes[:len(nodes)-1]
}

func getNodeStatus(c client.Client, node *corev1.Node) string {
	if node.Spec.Unschedulable {
		return "SchedulingDisabled"
	}

	machineAnnotation := node.ObjectMeta.Annotations["machine.openshift.io/machine"]

	machine := &machinev1beta1.Machine{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: strings.Split(machineAnnotation, "/")[1], Namespace: "openshift-machine-api"}, machine)
	if err != nil && errors.IsNotFound(err) {
		log.Info(fmt.Sprintf("Machine not found, assuming node deletion"))
		return "SchedulingDisabled"
	} else if err == nil {
		deletionTimestamp := machine.ObjectMeta.GetDeletionTimestamp()
		if deletionTimestamp != nil {
			log.Info(fmt.Sprintf("Machine with deletion timestamp, assuming node deletion"))
			return "SchedulingDisabled"
		}
	}

	for _, condition := range node.Status.Conditions {
		if condition.Type == "Ready" {
			if condition.Status == "True" {
				return "Ready"
			}
			return "NotReady"
		}
	}
	// This should not be possible
	return "None"
}

func getPreviousNodeStatus(nodeName string, instance *computenodev1alpha1.ComputeNodeOpenStack) string {
	for _, node := range instance.Status.Nodes {
		if node.Name == nodeName {
			return node.Status
		}
	}
	return "None"
}

func createBlockerPod(c client.Client, nodeName string, instance *computenodev1alpha1.ComputeNodeOpenStack) error {
	var terminationGracePeriodSeconds int64 = 0

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeName + "-blocker-pod",
			Namespace: instance.Namespace,
			Finalizers: []string{
				"compute-node.openstack.org/" + instance.Spec.RoleName,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:    nodeName + "-blocker-pod",
				Image:   "busybox",
				Command: []string{"/bin/sh", "-ec", "sleep infinity"},
			}},
			ServiceAccountName: instance.Spec.ServiceAccount,
			RestartPolicy:      "Always",
			HostNetwork:        true,
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": nodeName,
			},
			Tolerations: []corev1.Toleration{{
				Operator: "Exists",
			}},
			TerminationGracePeriodSeconds: &terminationGracePeriodSeconds,
		},
	}
	oref := metav1.NewControllerRef(instance, instance.GroupVersionKind())
	pod.SetOwnerReferences([]metav1.OwnerReference{*oref})
	if err := c.Create(context.TODO(), pod); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func isBlockerPodFinalizerDeleted(c client.Client, nodeName string, instance *computenodev1alpha1.ComputeNodeOpenStack) (bool, error) {
	podName := nodeName + "-blocker-pod"
	pod := &corev1.Pod{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: podName, Namespace: instance.Namespace}, pod)
	if err != nil && errors.IsNotFound(err) {
		return true, nil
	} else if err != nil {
		return false, err
	}

	if pod.Finalizers == nil {
		log.Info(fmt.Sprintf("The blocker pod finalizer was already removed. No need to do any further action for: %s", podName))
		return true, nil
	}
	return false, nil
}

func deleteBlockerPodFinalizer(c client.Client, nodeName string, instance *computenodev1alpha1.ComputeNodeOpenStack) error {
	podName := nodeName + "-blocker-pod"
	pod := &corev1.Pod{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: podName, Namespace: instance.Namespace}, pod)
	if err != nil && errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	//remove finalizer
	pod.Finalizers = []string{}
	err = c.Update(context.TODO(), pod)
	if err != nil {
		return err
	}
	log.Info(fmt.Sprintf("Node blocker pod deleted, node: %v, pod: %v", nodeName, podName))
	return nil
}

func ensureNodePreDrain(c client.Client, nodeName string, instance *computenodev1alpha1.ComputeNodeOpenStack, openstackClientAdmin *corev1.Secret, openstackClient *corev1.ConfigMap) (bool, error) {
	err := _triggerNodeDrain(c, nodeName, instance, true, openstackClientAdmin, openstackClient)
	if err != nil {
		return false, err
	}

	nodePreDrained, err := isNodeDrained(c, nodeName, nodeName+"-drain-job-pre", instance)
	if err != nil && errors.IsNotFound(err) {
		return false, fmt.Errorf("Pre NodeDrain job IsNotFound: %s", nodeName+"-drain-job-pre")
	} else if err != nil {
		return false, err
	}
	return nodePreDrained, nil
}

func ensureNodePostDrain(c client.Client, nodeName string, instance *computenodev1alpha1.ComputeNodeOpenStack, openstackClientAdmin *corev1.Secret, openstackClient *corev1.ConfigMap) (bool, error) {
	err := _triggerNodeDrain(c, nodeName, instance, false, openstackClientAdmin, openstackClient)
	if err != nil {
		return false, err
	}

	nodePostDrained, err := isNodeDrained(c, nodeName, nodeName+"-drain-job-post", instance)
	if err != nil && errors.IsNotFound(err) {
		return false, fmt.Errorf("Post NodeDrain job IsNotFound: %s", nodeName+"-drain-job-post")
	} else if err != nil {
		return false, err
	}
	return nodePostDrained, nil
}

func _triggerNodeDrain(c client.Client, nodeName string, instance *computenodev1alpha1.ComputeNodeOpenStack, runPreTasks bool, openstackClientAdmin *corev1.Secret, openstackClient *corev1.ConfigMap) error {
	var scriptsVolumeDefaultMode int32 = 0755

	envVars := []corev1.EnvVar{
		{
			Name:  "SCALE_DOWN_NODE_NAME",
			Value: nodeName,
		},
	}

	// Get all nodes where the machines tagged with the DeletionTimestamp
	deletedTaggedNodes, err := getDeletedOspWorkerNodes(c, instance)
	if err != nil {
		return err
	}
	// In case of CR delete the machines are not yet marked as deleted at this stage.
	// For now adding the current node to the deletedTaggedNodes
	// TODO: review this when we handle the owner ref change in non openstack namespace next.
	if len(deletedTaggedNodes) == 0 {
		deletedTaggedNodes = append(deletedTaggedNodes, nodeName)
	}
	envVars = append(envVars, corev1.EnvVar{Name: "DISABLE_COMPUTE_SERVICES", Value: strings.Join(deletedTaggedNodes, " ")})

	// Example to handle drain information
	enableLiveMigration := "false"
	if instance.Spec.Drain.Enabled {
		enableLiveMigration = fmt.Sprintf("%v", instance.Spec.Drain.Enabled)

	}

	for _, nodeToDelete := range instance.Spec.NodesToDelete {
		if nodeToDelete.Name == nodeName {
			if nodeToDelete.Drain {
				enableLiveMigration = fmt.Sprintf("%v", nodeToDelete.Drain)
			}
			break
		}
	}
	envVars = append(envVars, corev1.EnvVar{Name: "LIVE_MIGRATION_ENABLED", Value: enableLiveMigration})
	envVars = append(envVars, corev1.EnvVar{Name: "OS_CLOUD", Value: "default"})

	volumes := []corev1.Volume{
		{
			Name: strings.ToLower(instance.Kind) + "-scripts",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					DefaultMode: &scriptsVolumeDefaultMode,
					LocalObjectReference: corev1.LocalObjectReference{
						Name: strings.ToLower(instance.Kind) + "-scripts",
					},
				},
			},
		},
		{
			Name: "openstack-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: openstackClient.Name,
					},
				},
			},
		},
		{
			Name: "openstack-config-secret",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: openstackClientAdmin.Name,
				},
			},
		},
	}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      strings.ToLower(instance.Kind) + "-scripts",
			ReadOnly:  true,
			MountPath: "/usr/local/bin",
		},
		{
			Name:      "openstack-config",
			MountPath: "/etc/openstack/clouds.yaml",
			SubPath:   "clouds.yaml",
		},
		{
			Name:      "openstack-config-secret",
			MountPath: "/etc/openstack/secure.yaml",
			SubPath:   "secure.yaml",
		},
	}

	jobName := ""
	containerCommand := ""
	if runPreTasks {
		jobName = nodeName + "-drain-job-pre"
		containerCommand = "/usr/local/bin/scaledownpre.sh"
	} else {
		jobName = nodeName + "-drain-job-post"
		containerCommand = "/usr/local/bin/scaledownpost.sh"
	}

	var completions int32 = 1
	var parallelism int32 = 1
	var backoffLimit int32 = 20
	oref := metav1.NewControllerRef(instance, instance.GroupVersionKind())

	// Create job that runs the scale task
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:            jobName,
			Namespace:       instance.Namespace,
			OwnerReferences: []metav1.OwnerReference{*oref},
		},
		Spec: batchv1.JobSpec{
			Completions:  &completions,
			Parallelism:  &parallelism,
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Name:            jobName,
					Namespace:       instance.Namespace,
					OwnerReferences: []metav1.OwnerReference{*oref},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  jobName,
						Image: instance.Spec.Drain.DrainPodImage,
						// TODO: mschuppert - Right now this is not a kolla container.
						// When this is moved to a kolla based container, we should use
						// the kolla_start procedure
						Command:      []string{containerCommand},
						Env:          envVars,
						VolumeMounts: volumeMounts,
					}},
					Volumes:       volumes,
					RestartPolicy: "OnFailure",
				},
			},
		},
	}

	if err := c.Create(context.TODO(), job); err != nil && !errors.IsAlreadyExists(err) {
		return err
	} else if errors.IsAlreadyExists(err) {
		log.Info(fmt.Sprintf("Drain job already present: %v", jobName))
	} else {
		log.Info(fmt.Sprintf("Drain job created: %v", jobName))
	}
	return nil
}

func isNodeDrained(c client.Client, nodeName string, jobName string, instance *computenodev1alpha1.ComputeNodeOpenStack) (bool, error) {
	job := &batchv1.Job{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: jobName, Namespace: instance.Namespace}, job)
	if err != nil {
		return false, err
	}

	if job.Status.Succeeded == 1 {
		log.Info(fmt.Sprintf("NodeDrain job suceeded: %s", job.Name))
		return true, nil
	} else if job.Status.Active >= 1 {
		log.Info(fmt.Sprintf("NodeDrain job still running: %s", job.Name))
		return false, nil
	}

	// if job is not succeeded or active, return type and reason as error from first conditions
	// to log and reconcile
	conditionsType := ""
	conditionsReason := ""
	if len(job.Status.Conditions) > 0 {
		conditionsType = string(job.Status.Conditions[0].Type)
		conditionsReason = job.Status.Conditions[0].Reason
	}

	return false, fmt.Errorf("nodeDrain job type %v, reason: %v", conditionsType, conditionsReason)
}

func updateMachineDeletionSelection(c client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack) error {
	// We need to delete the old cluster-api-delete-machine labels and add the new ones
	nodesToDeleteInfo := make(map[string]string)
	for _, newNode := range instance.Spec.NodesToDelete {
		nodesToDeleteInfo[newNode.Name] = "Add"
	}
	for _, oldNode := range instance.Status.NodesToDelete {
		_, exists := nodesToDeleteInfo[oldNode.Name]
		if exists {
			delete(nodesToDeleteInfo, oldNode.Name)
		} else {
			nodesToDeleteInfo[oldNode.Name] = "Remove"
		}
	}
	machineList := &machinev1beta1.MachineList{}
	listOpts := []client.ListOption{
		client.InNamespace("openshift-machine-api"),
		client.MatchingLabels{"machine.openshift.io/cluster-api-machine-role": instance.Spec.RoleName},
	}
	err := c.List(context.TODO(), machineList, listOpts...)
	if err != nil {
		return err
	}
	annotations := map[string]string{}
	for _, machine := range machineList.Items {
		if machine.Status.NodeRef == nil {
			continue
		}

		machineNode := machine.Status.NodeRef.Name
		action, exists := nodesToDeleteInfo[machineNode]
		if !exists {
			continue
		}

		if action == "Add" {
			annotations["machine.openshift.io/cluster-api-delete-machine"] = "1"
			machine.SetAnnotations(annotations)
		} else if action == "Remove" {
			annotations["machine.openshift.io/cluster-api-delete-machine"] = "0"
			machine.SetAnnotations(annotations)
		}
		if err := c.Update(context.TODO(), &machine); err != nil {
			return err
		}
	}
	return nil
}

func ensureMachineSetSync(c client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack) (int32, error) {
	// get replicas at the openshift-machine-api machineset and return the current
	// available machine replicas if machineset is found
	workerMachineSet := &machinev1beta1.MachineSet{}
	machineSetName := instance.Spec.ClusterName + "-" + instance.Spec.RoleName
	var currentCount int32
	err := c.Get(context.TODO(), types.NamespacedName{Name: machineSetName, Namespace: "openshift-machine-api"}, workerMachineSet)
	if err != nil && errors.IsNotFound(err) {
		log.Info(fmt.Sprintf("Machineset not found, recreate it: %s osp-worker nodes: %d", machineSetName, instance.Spec.Workers))
		if err := c.Update(context.TODO(), instance); err != nil {
			return currentCount, err
		}
	} else if err != nil {
		return currentCount, err
	} else {
		currentCount = workerMachineSet.Status.AvailableReplicas
		if *workerMachineSet.Spec.Replicas != instance.Spec.Workers {
			// MachineSet has been updated, force CRD re-sync to match the machineset replicas
			instance.Spec.Workers = *workerMachineSet.Spec.Replicas
			if err := c.Update(context.TODO(), instance); err != nil {
				return currentCount, err
			}
		}
	}
	return currentCount, nil
}

func addToBeRemovedTaint(kclient kubernetes.Interface, node corev1.Node) error {
	var taintEffectNoExecute = corev1.TaintEffectNoExecute

	// check if the taint already exists
	for _, taint := range node.Spec.Taints {
		if taint.Key == "StopDaemonSetPods" {

			// don't need to re-add the taint
			log.Info(fmt.Sprintf("StopDaemonSetPods taint already present on node %v", node.Name))
			return nil
		}
	}

	node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
		Key:    "StopDaemonSetPods",
		Value:  fmt.Sprint(time.Now().Unix()),
		Effect: taintEffectNoExecute,
	})

	updatedNode, err := kclient.CoreV1().Nodes().Update(context.TODO(), &node, metav1.UpdateOptions{})
	if err != nil || updatedNode == nil {
		return fmt.Errorf("failed to update node %v after adding taint: %v", node.Name, err)
	}

	log.Info(fmt.Sprintf("StopDaemonSetPods taint added on node %v", node.Name))
	return nil
}

func deleteJobWithName(c client.Client, kclient kubernetes.Interface, instance *computenodev1alpha1.ComputeNodeOpenStack, jobName string) error {
	job := &batchv1.Job{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: jobName, Namespace: instance.Namespace}, job)
	if err != nil {
		return err
	}

	if job.Status.Succeeded == 1 {
		background := metav1.DeletePropagationBackground
		err = kclient.BatchV1().Jobs(instance.Namespace).Delete(context.TODO(), jobName, metav1.DeleteOptions{PropagationPolicy: &background})
		if err != nil {
			return fmt.Errorf("failed to delete drain job: %s, %v", job.Name, err)
		}
		log.Info("Deleted NodeDrain job: " + job.Name)
	}

	return nil
}

func ensureComputeNodeOpenStackScriptsConfigMap(r *ComputeNodeOpenStackReconciler, instance *computenodev1alpha1.ComputeNodeOpenStack, name string) error {
	scriptsConfigMap := computenodeopenstack.ScriptsConfigMap(instance, name)

	// Check if this ScriptsConfigMap already exists
	foundScriptsConfigMap := &corev1.ConfigMap{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: scriptsConfigMap.Name, Namespace: scriptsConfigMap.Namespace}, foundScriptsConfigMap)
	if err != nil && errors.IsNotFound(err) {
		log.Info(fmt.Sprintf("Creating a new ScriptsConfigMap - Namespace: %s, Config Map name: %s", scriptsConfigMap.Namespace, scriptsConfigMap.Name))
		if err := controllerutil.SetControllerReference(instance, scriptsConfigMap, r.Scheme); err != nil {
			return err
		}

		err = r.Client.Create(context.TODO(), scriptsConfigMap)
		if err != nil {
			return err
		}
	} else {
		// Calc hashes of existing ConfigMap and Data coming with image to see if we need to update
		// the scripts in the existing ConfigMap
		scriptsConfigMapDataHash, err := libcommonutil.ObjectHash(scriptsConfigMap.Data)
		if err != nil {
			return fmt.Errorf("error calculating scriptsConfigMapDataHash hash: %v", err)
		}

		foundScriptsConfigMapDataHash, err := libcommonutil.ObjectHash(foundScriptsConfigMap.Data)
		if err != nil {
			return fmt.Errorf("error calculating foundScriptsConfigMapDataHash hash: %v", err)
		}

		if scriptsConfigMapDataHash != foundScriptsConfigMapDataHash {
			log.Info(fmt.Sprintf("Updating ScriptsConfigMap"))
			// if scripts got update in operator image, also update the existing configmap
			foundScriptsConfigMap.Data = scriptsConfigMap.Data
			err = r.Client.Update(context.TODO(), foundScriptsConfigMap)
			if err != nil {
				return err
			}
			log.Info(fmt.Sprintf("ScriptsConfigMap updated - New Data Hash: %s", scriptsConfigMapDataHash))
		}
	}
	return nil
}

func getOpenStackClientInformation(r *ComputeNodeOpenStackReconciler, instance *computenodev1alpha1.ComputeNodeOpenStack) (*corev1.Secret, *corev1.ConfigMap, error) {
	openStackClientAdminSecret := instance.Spec.OpenStackClientAdminSecret
	openStackClientConfigMap := instance.Spec.OpenStackClientConfigMap

	// check for Secret with information required for scale down tasks to connect to the OpenStack API
	openstackClientAdmin := &corev1.Secret{}
	// Check if secret holding the admin information for connection to the api endpoints exist
	err := r.Client.Get(context.TODO(), types.NamespacedName{Name: openStackClientAdminSecret, Namespace: instance.Namespace}, openstackClientAdmin)
	if err != nil && errors.IsNotFound(err) {
		return nil, nil, fmt.Errorf("Failed to find the secret %s holding admin user information required to connect to the OSP API endpoints: %v", openStackClientAdminSecret, err)
	}

	// check for ConfigMap with information required for scale down tasks to connect to the OpenStack API
	openstackClient := &corev1.ConfigMap{}
	// Check if configmap holding the information for connection to the api endpoints exist
	err = r.Client.Get(context.TODO(), types.NamespacedName{Name: openStackClientConfigMap, Namespace: instance.Namespace}, openstackClient)
	if err != nil && errors.IsNotFound(err) {
		return nil, nil, fmt.Errorf("Failed to find the configmap %s holding information required to connect to the OSP API endpoints: %v", openStackClientConfigMap, err)
	}
	return openstackClientAdmin, openstackClient, nil
}

func getDeletedOspWorkerNodes(c client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack) ([]string, error) {
	deletedOspWorkerNodes := []string{}
	machineList := &machinev1beta1.MachineList{}
	listOpts := []client.ListOption{
		client.InNamespace("openshift-machine-api"),
		client.MatchingLabels{"machine.openshift.io/cluster-api-machine-role": instance.Spec.RoleName},
	}
	err := c.List(context.TODO(), machineList, listOpts...)
	if err != nil {
		return nil, err
	}

	for _, machine := range machineList.Items {
		if machine.Status.NodeRef == nil {
			continue
		}
		deletionTimestamp := machine.DeletionTimestamp
		if deletionTimestamp != nil {
			deletedOspWorkerNodes = append(deletedOspWorkerNodes, machine.Status.NodeRef.Name)
		}
	}
	return deletedOspWorkerNodes, nil
}

func deleteAllBlockerPodFinalizer(r *ComputeNodeOpenStackReconciler, instance *computenodev1alpha1.ComputeNodeOpenStack) error {
	// get all nodes for the current role
	workerLabel := "node-role.kubernetes.io/" + instance.Spec.RoleName
	nodeList, err := r.Kclient.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{LabelSelector: workerLabel})
	if err != nil {
		return err
	}

	// if nodes already got scaled down, no need to run cleanup
	if len(nodeList.Items) == 0 {
		return nil
	}

	// get the openstack client information required by the POST job to connect to the OSP api
	openStackClientAdmin, openStackClient, err := getOpenStackClientInformation(r, instance)
	if err != nil {
		return err
	}

	/* Steps to delete the drain the node
	1. Predraining: disable nova service, (optional) migrate VMs, wait until there is no VMs
	2. Postdraining: taint the node, remove nova-compute from nova services and placement
	3. Remove cleanup jobs, blocker pod finalizer, and update nodesToDelete status information
	*/
	// 1. NodePreDrain
	allNodesPreDrained := true
	for _, node := range nodeList.Items {
		// If blocker pod finalizer was already deleted, there is nothing extra to do for this node
		finalizerDeleted, err := isBlockerPodFinalizerDeleted(r.Client, node.Name, instance)
		if err != nil {
			return err
		} else if finalizerDeleted {
			continue
		}

		nodePreDrained, err := ensureNodePreDrain(r.Client, node.Name, instance, openStackClientAdmin, openStackClient)
		if err != nil && !errors.IsAlreadyExists(err) {
			return err
		}
		if !nodePreDrained {
			allNodesPreDrained = false
			continue
		}
		log.Info(fmt.Sprintf("Pre draining job completed for node: %s", node.Name))
	}
	if !allNodesPreDrained {
		// if not all Pre draining jobs finished, return a StatusReasonAlreadyExists.
		// AlreadyExists error will be ignored as we want to wait for the job event to
		// not spam the operator log with too many not needed reconcile requrest.
		return &errors.StatusError{ErrStatus: metav1.Status{
			Status: metav1.StatusFailure,
			Code:   http.StatusConflict,
			Reason: metav1.StatusReasonAlreadyExists,
			Details: &metav1.StatusDetails{
				Group: instance.GroupVersionKind().Group,
				Kind:  instance.Kind,
				Name:  instance.Name,
			},
			Message: fmt.Sprintf("Not all Pre NodeDrain jobs finished"),
		}}
	}
	log.Info(fmt.Sprintf("All Pre NodeDrain jobs finished"))

	// 2. NodePostDrain
	allNodesPostDrained := true
	for _, node := range nodeList.Items {
		// If blocker pod finalizer was already deleted, there is nothing extra to do for this node
		finalizerDeleted, err := isBlockerPodFinalizerDeleted(r.Client, node.Name, instance)
		if err != nil {
			return err
		} else if finalizerDeleted {
			continue
		}

		// a) taint all the nodes to have OSP pods stopped
		err = addToBeRemovedTaint(r.Kclient, node)
		if err != nil {
			return err
		}

		// b) run post drain job to get the nova-compute service cleanup up in the OSP control plane
		nodePostDrained, err := ensureNodePostDrain(r.Client, node.Name, instance, openStackClientAdmin, openStackClient)
		if err != nil && !errors.IsAlreadyExists(err) {
			return err
		}
		if !nodePostDrained {
			allNodesPostDrained = false
			continue
		}
		log.Info(fmt.Sprintf("Post draining job completed for node: %s", node.Name))
	}
	if !allNodesPostDrained {
		// if not all Post draining jobs finished, return a StatusReasonAlreadyExists.
		// AlreadyExists error will be ignored as we want to wait for the job event to
		// not spam the operator log with too many not needed reconcile requrest.
		return &errors.StatusError{ErrStatus: metav1.Status{
			Status: metav1.StatusFailure,
			Code:   http.StatusConflict,
			Reason: metav1.StatusReasonAlreadyExists,
			Details: &metav1.StatusDetails{
				Group: instance.GroupVersionKind().Group,
				Kind:  instance.Kind,
				Name:  instance.Name,
			},
			Message: fmt.Sprintf("Not all POST NodeDrain jobs finished"),
		}}
	}
	log.Info(fmt.Sprintf("All Post NodeDrain jobs finished"))

	// 3. Cleanup
	for _, node := range nodeList.Items {
		// If blocker pod finalizer was already deleted, there is nothing extra to do for this node
		finalizerDeleted, err := isBlockerPodFinalizerDeleted(r.Client, node.Name, instance)
		if err != nil {
			return err
		} else if finalizerDeleted {
			continue
		}

		// delete drain jobs
		err = deleteJobWithName(r.Client, r.Kclient, instance, node.Name+"-drain-job-post")
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		log.Info(fmt.Sprintf("Deleted POST draining jobs for node: %s", node.Name))
		err = deleteJobWithName(r.Client, r.Kclient, instance, node.Name+"-drain-job-pre")
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		log.Info(fmt.Sprintf("Deleted PRE draining jobs for node: %s", node.Name))

		// delete blocker pods to proceed with node removal from OCP
		log.Info(fmt.Sprintf("Deleting blocker pod finalizer for node: %s", node.Name))
		err = deleteBlockerPodFinalizer(r.Client, node.Name, instance)
		if err != nil {
			return err
		}
	}
	return nil
}

func ensureSriovRemovalSync(c client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack) error {
	labelSelectorMap := map[string]string{
		ownerUIDLabelSelector:       string(instance.UID),
		ownerNameSpaceLabelSelector: instance.Namespace,
		ownerNameLabelSelector:      instance.Name,
	}

	// Get all SRIOV policies tied to this instance
	sriovNetworkNodePolicies, err := computenodeopenstack.GetSriovNetworkNodePoliciesWithLabel(c, instance, labelSelectorMap, "openshift-sriov-network-operator")
	if err != nil {
		return err
	}

	findInterface := func(instance *computenodev1alpha1.ComputeNodeOpenStack, interfaceName string) bool {
		for _, sriovConfig := range instance.Spec.Network.Sriov {
			if sriovConfig.Interface == interfaceName {
				return true
			}
		}

		return false
	}

	// Iterate through SRIOV policies and delete those that have no interfaces represented
	// in the instance's Network.Sriov spec
	for idx := range sriovNetworkNodePolicies.Items {
		snnp := &sriovNetworkNodePolicies.Items[idx]

		foundInterface := false

		for _, interfaceName := range snnp.Spec.NicSelector.PfNames {
			foundInterface = findInterface(instance, interfaceName)

			if foundInterface {
				break
			}
		}

		if !foundInterface {
			err = c.Delete(context.Background(), snnp, &client.DeleteOptions{})
			if err != nil {
				return err
			}
			log.Info(fmt.Sprintf("Deleting SriovNetworkNodePolicy that does not match instance SRIOV interfaces: name %s - %s", snnp.Name, snnp.UID))
		}
	}

	return nil
}

/* deleteOwnerRefLabeledObjects - cleans up namespaced objects outside the default namespace
   using the owner reference labels added.
   List of objects which get cleaned:
   - infraDaemonSets, namespace information from CR.Spec + CR.Status
   - machineset, openshift-machine-api namespace
   - user-data secret, openshift-machine-api namespace
   - machineconfigpool, not namespaced
   - machineconfig, not namespaced
   - sriovnetworknodepolicy, openshift-sriov-network-operator namespace
*/
func deleteOwnerRefLabeledObjects(r *ComputeNodeOpenStackReconciler, instance *computenodev1alpha1.ComputeNodeOpenStack) error {
	labelSelectorMap := map[string]string{
		ownerUIDLabelSelector:       string(instance.UID),
		ownerNameSpaceLabelSelector: instance.Namespace,
		ownerNameLabelSelector:      instance.Name,
	}
	labelSelectorString := labels.Set(labelSelectorMap).String()

	// remove infra daemonsets with owner reference label
	infraDaemonSets := instance.Status.InfraDaemonSets
	infraDaemonSets = append(infraDaemonSets, instance.Spec.InfraDaemonSets...)
	for _, statusDsInfo := range infraDaemonSets {
		err := r.Kclient.AppsV1().DaemonSets(statusDsInfo.Namespace).DeleteCollection(context.TODO(), metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: labelSelectorString})
		if err != nil && errors.IsNotFound(err) {
			// Already deleted
			continue
		} else if err != nil {
			return err
		}
		log.Info(fmt.Sprintf("Infra Daemonset deleted - name: %s - namespace: %s", statusDsInfo.Name+"-"+instance.Spec.RoleName, statusDsInfo.Namespace))
	}

	// delete machineset in openshift-machine-api namespace
	machineSets, err := computenodeopenstack.GetMachineSetsWithLabel(r.Client, instance, labelSelectorMap, "openshift-machine-api")
	if err != nil {
		return err
	}
	for idx := range machineSets.Items {
		ms := &machineSets.Items[idx]

		err = r.Client.Delete(context.Background(), ms, &client.DeleteOptions{})
		if err != nil {
			return err
		}
		log.Info(fmt.Sprintf("MachineSet deleted: name %s - %s", ms.Name, ms.UID))
	}

	// delete user-data secret in openshift-machine-api namespace
	err = r.Kclient.CoreV1().Secrets("openshift-machine-api").DeleteCollection(context.TODO(), metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: labelSelectorString})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	log.Info(fmt.Sprintf("Machine user data Secret deleted for %s - %s", instance.Name, instance.Spec.RoleName))

	// delete machineconfigpool, not namespaced
	machineConfigPools, err := computenodeopenstack.GetMachineConfigPoolWithLabel(r.Client, instance, labelSelectorMap)
	if err != nil {
		return err
	}
	for idx := range machineConfigPools.Items {
		mcp := &machineConfigPools.Items[idx]

		err = r.Client.Delete(context.Background(), mcp, &client.DeleteOptions{})
		if err != nil {
			return err
		}
		log.Info(fmt.Sprintf("MachineConfigPool deleted: name %s - %s", mcp.Name, mcp.UID))
	}

	// delete machineconfigpool, not namespaced
	machineConfigs, err := computenodeopenstack.GetMachineConfigWithLabel(r.Client, instance, labelSelectorMap)
	if err != nil {
		return err
	}
	for idx := range machineConfigs.Items {
		mc := &machineConfigs.Items[idx]

		err = r.Client.Delete(context.Background(), mc, &client.DeleteOptions{})
		if err != nil {
			return err
		}
		log.Info(fmt.Sprintf("MachineConfig deleted: name %s - %s", mc.Name, mc.UID))
	}

	// delete sriovnetworknodepolicies in openshift-sriov-network-operator namespace
	sriovNetworkNodePolicies, err := computenodeopenstack.GetSriovNetworkNodePoliciesWithLabel(r.Client, instance, labelSelectorMap, "openshift-sriov-network-operator")
	if err != nil {
		return err
	}
	for idx := range sriovNetworkNodePolicies.Items {
		snnp := &sriovNetworkNodePolicies.Items[idx]

		err = r.Client.Delete(context.Background(), snnp, &client.DeleteOptions{})
		if err != nil {
			return err
		}
		log.Info(fmt.Sprintf("SriovNetworkNodePolicy deleted: name %s - %s", snnp.Name, snnp.UID))
	}

	return nil
}
