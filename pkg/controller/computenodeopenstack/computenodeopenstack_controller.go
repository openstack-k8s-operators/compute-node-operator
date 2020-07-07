package computenodeopenstack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	computenodev1alpha1 "github.com/openstack-k8s-operators/compute-node-operator/pkg/apis/computenode/v1alpha1"
	bindatautil "github.com/openstack-k8s-operators/compute-node-operator/pkg/bindata_util"
	computenodeopenstack "github.com/openstack-k8s-operators/compute-node-operator/pkg/computenodeopenstack"
	util "github.com/openstack-k8s-operators/compute-node-operator/pkg/util"
	libcommonutil "github.com/openstack-k8s-operators/lib-common/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	uns "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	machinev1beta1 "github.com/openshift/cluster-api/pkg/apis/machine/v1beta1"
)

var log = logf.Log.WithName("controller_computenodeopenstack")

const (
	machineRoleKey                     = "machine.openshift.io/cluster-api-machine-role"
	disableNovaComputeServiceJobPrefix = "disable-nova-compute-service-"
)

// ManifestPath is the path to the manifest templates
var ManifestPath = "./bindata"

// ResyncPeriod - The periodic resync interval.
// We will re-run the reconciliation logic, even if the configuration hasn't changed.
var ResyncPeriod = 10 * time.Minute

// Add creates a new ComputeNodeOpenStack Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	cfg, err := config.GetConfig()
	if err != nil {
		log.Error(err, "")
		os.Exit(1)
	}
	kclient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Error(err, "")
		os.Exit(1)
	}
	return &ReconcileComputeNodeOpenStack{client: mgr.GetClient(), scheme: mgr.GetScheme(), kclient: kclient}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("computenodeopenstack-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource ComputeNodeOpenStack
	err = c.Watch(&source.Kind{Type: &computenodev1alpha1.ComputeNodeOpenStack{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &machinev1beta1.MachineSet{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &computenodev1alpha1.ComputeNodeOpenStack{},
	})
	if err != nil {
		return err
	}

	err = c.Watch(&source.Kind{Type: &batchv1.Job{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &computenodev1alpha1.ComputeNodeOpenStack{},
	})
	if err != nil {
		return err
	}

	// Watch ConfigMaps owned by ComputeNodeOpenStack
	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &computenodev1alpha1.ComputeNodeOpenStack{},
	})
	if err != nil {
		return err
	}

	// watch for machine delete events and filter on machineset with ref.Kind == ComputeNodeOpenStack
	deletedOspWorkersFn := handler.ToRequestsFunc(func(o handler.MapObject) []reconcile.Request {
		cc := mgr.GetClient()
		result := []reconcile.Request{}
		m := &machinev1beta1.Machine{}
		key := client.ObjectKey{Namespace: o.Meta.GetNamespace(), Name: o.Meta.GetName()}
		err := cc.Get(context.Background(), key, m)
		if err != nil && !errors.IsNotFound(err) {
			log.Error(err, "Unable to retrieve Machine %v")
			return nil
		}

		deletionTimestamp := o.Meta.GetDeletionTimestamp()
		if deletionTimestamp != nil {
			mss := util.GetMachineSetsForMachine(cc, m)
			if len(mss) == 0 {
				log.Info(fmt.Sprintf("Machine %s could not be mapped to any machineset", o.Meta.GetName()))
				return nil
			}
			for _, ms := range mss {
				for _, ref := range ms.ObjectMeta.GetOwnerReferences() {

					if ref.Controller != nil && ref.Kind == "ComputeNodeOpenStack" {
						log.Info(fmt.Sprintf("Machine object %s marked for deletion: %s", o.Meta.GetName(), deletionTimestamp))
						// return namespace and ref.Name
						name := client.ObjectKey{
							Namespace: ms.Namespace,
							Name:      ref.Name,
						}
						result = append(result, reconcile.Request{NamespacedName: name})
					}
				}
			}
			return result
		}
		return nil
	})

	err = c.Watch(&source.Kind{Type: &machinev1beta1.Machine{}}, &handler.EnqueueRequestsFromMapFunc{
		ToRequests: deletedOspWorkersFn,
	})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileComputeNodeOpenStack implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileComputeNodeOpenStack{}

// ReconcileComputeNodeOpenStack reconciles a ComputeNodeOpenStack object
type ReconcileComputeNodeOpenStack struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client  client.Client
	scheme  *runtime.Scheme
	kclient kubernetes.Interface
}

// Reconcile reads that state of the cluster for a ComputeNodeOpenStack object and makes changes based on the state read
// and what is in the ComputeNodeOpenStack.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileComputeNodeOpenStack) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling ComputeNodeOpenStack")

	// Fetch the ComputeNodeOpenStack instance
	instance := &computenodev1alpha1.ComputeNodeOpenStack{}
	err := r.client.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	// ScriptsConfigMap
	err = ensureComputeNodeOpenStackScriptsConfigMap(r, instance, strings.ToLower(instance.Kind)+"-scripts")
	if err != nil {
		return reconcile.Result{}, err
	}

	// openStackClientAdminSecret and openStackClientConfigMap are expected to exist
	// mschuppert - note: 	the format of the secret/configmap might change when the OSP controller
	//			operators who in the end should populate this information are done. So
	//			likely changes are expected.
	openStackClientAdmin, openStackClient, err := getOpenStackClientInformation(r, instance)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Disable nova-compute service early if nodes are tagged as deleted
	disableWorkerNodes, err := getDeletedOspWorkerNodes(r.client, instance)
	if err != nil {
		return reconcile.Result{}, err
	}
	if len(disableWorkerNodes) > 0 && !reflect.DeepEqual(instance.Status.DisabledNodes, disableWorkerNodes) {
		// run job to disable nova-compute service in OSP
		err := disableNovaComputeService(r.client, disableWorkerNodes, instance, openStackClientAdmin, openStackClient)
		if err != nil {
			return reconcile.Result{}, err
		}
		// add disableWorkerNodes to CR status to not rerun if no new nodes got flagged to be deleted
		// sort node information to not change status if order changes
		sort.Slice(disableWorkerNodes, func(i, j int) bool {
			return disableWorkerNodes[i].Name < disableWorkerNodes[j].Name
		})
		instance.Status.DisabledNodes = disableWorkerNodes
		err = r.client.Status().Update(context.TODO(), instance)
		if err != nil {
			return reconcile.Result{}, err
		}
	} else if len(disableWorkerNodes) == 0 {
		// remove deleted nodes from CR status
		instance.Status.DisabledNodes = []computenodev1alpha1.DisabledNode{}
		err = r.client.Status().Update(context.TODO(), instance)
		if err != nil {
			return reconcile.Result{}, err
		}
		// delete previous finished disable-nova-compute-service job
		disableComputeServiceJobName := disableNovaComputeServiceJobPrefix + instance.Spec.RoleName
		err = util.DeleteJobWithName(r.client, r.kclient, instance, disableComputeServiceJobName)
		if err != nil && !errors.IsNotFound(err) {
			return reconcile.Result{}, err
		}
	}

	specMDS, err := util.CalculateHash(instance.Spec)
	if err != nil {
		return reconcile.Result{}, err
	}
	if reflect.DeepEqual(specMDS, instance.Status.SpecMDS) {
		err = ensureMachineSetSync(r.client, instance)
		if err != nil {
			return reconcile.Result{}, err
		}
		err := updateNodesStatus(r.client, r.kclient, instance, openStackClientAdmin, openStackClient)
		if err != nil {
			return reconcile.Result{}, err
		}
	} else if !reflect.DeepEqual(instance.Spec.NodesToDelete, instance.Status.NodesToDelete) || instance.Spec.Workers < instance.Status.Workers {
		// Check if nodes to delete information has changed
		err = updateMachineDeletionSelection(r.client, instance)
		if err != nil {
			return reconcile.Result{}, err
		}
	}

	// Need to reapply the spec
	// Fill all defaults explicitly
	data, err := getRenderData(context.TODO(), r.client, instance)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Generate the Worker objects
	objs := []*uns.Unstructured{}
	manifests, err := bindatautil.RenderDir(filepath.Join(ManifestPath, "worker-osp"), &data)
	if err != nil {
		log.Error(err, "Failed to render worker manifests : %v")
		return reconcile.Result{}, err
	}
	objs = append(objs, manifests...)

	// Generate the Compute objects
	manifests, err = bindatautil.RenderDir(filepath.Join(ManifestPath, "nova"), &data)
	if err != nil {
		log.Error(err, "Failed to render nova manifests : %v")
		return reconcile.Result{}, err
	}
	objs = append(objs, manifests...)

	// Generate the Neutron objects
	manifests, err = bindatautil.RenderDir(filepath.Join(ManifestPath, "neutron"), &data)
	if err != nil {
		log.Error(err, "Failed to render neutron manifests : %v")
		return reconcile.Result{}, err
	}
	objs = append(objs, manifests...)

	// Apply the objects to the cluster
	for _, obj := range objs {
		// Set ComputeOpenStack instance as the owner and controller
		oref := metav1.NewControllerRef(instance, instance.GroupVersionKind())
		obj.SetOwnerReferences([]metav1.OwnerReference{*oref})

		// Open question: should an error here indicate we will never retry?
		if err := bindatautil.ApplyObject(context.TODO(), r.client, obj); err != nil {
			log.Error(err, "Failed to apply objects")
			return reconcile.Result{}, err
		}
	}

	/* Only create the new ones, deleting the old ones not needed anymore
	// create node-exporter daemonset (to have monitoring information)
	// create machine-config-daemon daemonset(to allow reconfigurations)
	// create multus daemonset (to set the node to ready)
	*/
	if err := ensureInfraDaemonsets(context.TODO(), r.kclient, instance); err != nil {
		log.Error(err, "Failed to create the infra daemon sets")
		return reconcile.Result{}, err
	}
	reqLogger.Info("Created InfraDaemonSets")

	// Update Status
	instance.Status.Workers = instance.Spec.Workers
	instance.Status.InfraDaemonSets = instance.Spec.InfraDaemonSets
	instance.Status.NodesToDelete = instance.Spec.NodesToDelete
	instance.Status.SpecMDS = specMDS
	err = r.client.Status().Update(context.TODO(), instance)
	if err != nil {
		log.Error(err, "Failed to update CR status %v")
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
}

func getRenderData(ctx context.Context, client client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack) (bindatautil.RenderData, error) {
	data := bindatautil.MakeRenderData()
	data.Data["ClusterName"] = instance.Spec.ClusterName
	data.Data["WorkerOspRole"] = instance.Spec.RoleName
	data.Data["K8sServiceIP"] = instance.Spec.K8sServiceIP
	data.Data["APIIntIP"] = instance.Spec.APIIntIP
	data.Data["Workers"] = instance.Spec.Workers

	data.Data["Isolcpus"] = false
	data.Data["SshdPort"] = 2022
	data.Data["NovaComputeCPUDedicatedSet"] = ""
	data.Data["NovaComputeCPUSharedSet"] = ""
	data.Data["CommonConfigMap"] = "common-config"
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
	if instance.Spec.Compute.CommonConfigMap != "" {
		data.Data["CommonConfigMap"] = instance.Spec.Compute.CommonConfigMap
	}
	if instance.Spec.Compute.OspSecrets != "" {
		data.Data["OspSecrets"] = instance.Spec.Compute.OspSecrets
	}

	data.Data["Nic"] = "enp6s0"
	if instance.Spec.Network.Nic != "" {
		data.Data["Nic"] = instance.Spec.Network.Nic
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

	return data, nil
}

func ensureInfraDaemonsets(ctx context.Context, client kubernetes.Interface, instance *computenodev1alpha1.ComputeNodeOpenStack) error {
	// Create/Update the required ones
	for _, dsInfo := range instance.Spec.InfraDaemonSets {
		originDaemonSet := &appsv1.DaemonSet{}
		originDaemonSet, err := client.AppsV1().DaemonSets(dsInfo.Namespace).Get(dsInfo.Name, metav1.GetOptions{})
		if err != nil && errors.IsNotFound(err) {
			log.Error(err, "Failed to find the daemon set", dsInfo.Name, dsInfo.Namespace)
			return err
		} else if err != nil {
			return err
		} else {
			ospDaemonSet := &appsv1.DaemonSet{}
			ospDaemonSetName := dsInfo.Name + "-" + instance.Spec.RoleName
			ospDaemonSet, err = client.AppsV1().DaemonSets(dsInfo.Namespace).Get(ospDaemonSetName, metav1.GetOptions{})
			if err != nil && errors.IsNotFound(err) {
				// Creating a new Daemonset ospDaemonSetName
				ds := newDaemonSet(instance, originDaemonSet, ospDaemonSetName, dsInfo.Namespace)
				_, err := client.AppsV1().DaemonSets(dsInfo.Namespace).Create(ds)
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
					_, err := client.AppsV1().DaemonSets(dsInfo.Namespace).Update(ds)
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
			err := client.AppsV1().DaemonSets(statusDsInfo.Namespace).Delete(ospDaemonSetName, &metav1.DeleteOptions{})
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
		},
		Spec: ds.Spec,
	}

	// Set OwnerReference
	oref := metav1.NewControllerRef(instance, instance.GroupVersionKind())
	daemonSet.SetOwnerReferences([]metav1.OwnerReference{*oref})

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

// updateNodesStatus - return true if we should reconcil. Right now this is set when
// we scale down and not all nodes are in SchedulingDisabled state.
func updateNodesStatus(c client.Client, kclient kubernetes.Interface, instance *computenodev1alpha1.ComputeNodeOpenStack, openstackClientAdmin *corev1.Secret, openstackClient *corev1.ConfigMap) error {

	// With the operator watching multiple namespaces provided as a list the c client
	// returns each entry multiple times. The kclient returns only a single entry.
	workerLabel := "node-role.kubernetes.io/" + instance.Spec.RoleName
	nodeList, err := kclient.CoreV1().Nodes().List(metav1.ListOptions{LabelSelector: workerLabel})
	if err != nil {
		return err
	}

	var readyWorkers int32 = 0
	nodesStatus := []computenodev1alpha1.Node{}
	for _, node := range nodeList.Items {
		log.Info(fmt.Sprintf("Update node status for node: %s", node.Name))
		nodeStatus := getNodeStatus(c, &node)
		previousNodeStatus := getPreviousNodeStatus(node.Name, instance)

		switch nodeStatus {
		case "NotReady":
			log.Info(fmt.Sprintf("Node status is NotReady: %s", node.Name))
			if previousNodeStatus == "None" {
				// Create blocker pod
				err = createBlockerPod(c, node.Name, instance)
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
				err = createBlockerPod(c, node.Name, instance)
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
			// If blocker pod finalizer was already deleted, there is nothing extra to do either
			finalizerDeleted, err := isBlockerPodFinalizerDeleted(c, node.Name, instance)
			if err != nil {
				return err
			} else if finalizerDeleted {
				break
			}

			// Wait until job to disable nova-compute services finished
			disableComputeServiceJobName := disableNovaComputeServiceJobPrefix + instance.Spec.RoleName
			disableComputeServiceJobCompleted, err := util.IsJobDone(c, disableComputeServiceJobName, instance)
			if err != nil && errors.IsNotFound(err) {
				return fmt.Errorf("Disable nova-compute job IsNotFound: %s", disableComputeServiceJobName)
			} else if err != nil {
				return err
			}
			if !disableComputeServiceJobCompleted {
				// add node to status
				newNode := computenodev1alpha1.Node{Name: node.Name, Status: nodeStatus}
				nodesStatus = append(nodesStatus, newNode)
				break
			}
			log.Info(fmt.Sprintf("Disable compute service job completed: %t", disableComputeServiceJobCompleted))

			/* Steps to delete the drain the node
			1. Predraining: verify nova service is disabled, (optional) migrate VMs, wait until there is no VMs
			2. Postdraining: taint the node, remove nova-compute from nova services and placement
			3. Remove cleanup jobs, blocker pod finalizer, and update nodesToDelete status information
			*/
			// 1. NodePreDrain
			nodePreDrained, err := ensureNodePreDrain(c, node.Name, instance, openstackClientAdmin, openstackClient)
			if err != nil {
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
			err = addToBeRemovedTaint(kclient, node)
			if err != nil {
				return err
			}
			// b) run post drain job
			nodePostDrained, err := ensureNodePostDrain(c, node.Name, instance, openstackClientAdmin, openstackClient)
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
			err = util.DeleteJobWithName(c, kclient, instance, node.Name+"-drain-job-post")
			if err != nil && !errors.IsNotFound(err) {
				return err
			}
			err = util.DeleteJobWithName(c, kclient, instance, node.Name+"-drain-job-pre")
			if err != nil && !errors.IsNotFound(err) {
				return err
			}

			log.Info(fmt.Sprintf("Deleting blocker pod finalizer for node: %s", node.Name))
			// delete blocker pod to proceed with node removal from OCP
			err = deleteBlockerPodFinalizer(c, node.Name, instance)
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
					err = c.Update(context.TODO(), instance)
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
		err = c.Status().Update(context.TODO(), instance)
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
			RestartPolicy: "Always",
			HostNetwork:   true,
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": nodeName,
			},
			Tolerations: []corev1.Toleration{{
				Operator: "Exists",
			}},
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

	nodePreDrained, err := util.IsJobDone(c, nodeName+"-drain-job-pre", instance)
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

	nodePostDrained, err := util.IsJobDone(c, nodeName+"-drain-job-post", instance)
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

	// Env vars to connect to openstack endpoints
	// mschuppert - note: 	the format of the secret/configmap might change when the OSP controller
	//			operators who in the end should populate this information are done. So
	//			likely changes are expected.
	// From ConfigMap
	for env, v := range openstackClient.Data {
		envVars = append(envVars, corev1.EnvVar{Name: env, Value: v})
	}
	// From Secret
	for env, v := range openstackClientAdmin.Data {
		envVars = append(envVars, corev1.EnvVar{Name: env, Value: string(v)})
	}

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
	}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      strings.ToLower(instance.Kind) + "-scripts",
			ReadOnly:  true,
			MountPath: "/usr/local/bin",
		},
	}

	jobName := ""
	containerCommand := []string{}
	if runPreTasks {
		jobName = nodeName + "-drain-job-pre"
		containerCommand = append(containerCommand, "/usr/local/bin/scaledownpre.sh")
	} else {
		jobName = nodeName + "-drain-job-post"
		containerCommand = append(containerCommand, "/usr/local/bin/scaledownpost.sh")
	}

	// Create job that runs the scale task
	err := util.CreateJob(c, instance, jobName, volumeMounts, volumes, containerCommand, envVars)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	} else if errors.IsAlreadyExists(err) {
		log.Info(fmt.Sprintf("Job already present: %v", jobName))
	} else {
		log.Info(fmt.Sprintf("Job created: %v", jobName))
	}

	return nil
}

func disableNovaComputeService(c client.Client, deletedTaggedMachineNames []computenodev1alpha1.DisabledNode, instance *computenodev1alpha1.ComputeNodeOpenStack, openstackClientAdmin *corev1.Secret, openstackClient *corev1.ConfigMap) error {
	var scriptsVolumeDefaultMode int32 = 0755
	envVars := []corev1.EnvVar{}

	// Env vars to connect to openstack endpoints
	// mschuppert - note: 	the format of the secret/configmap might change when the OSP controller
	//			operators who in the end should populate this information are done. So
	//			likely changes are expected.
	// From ConfigMap
	for env, v := range openstackClient.Data {
		envVars = append(envVars, corev1.EnvVar{Name: env, Value: v})
	}
	// From Secret
	for env, v := range openstackClientAdmin.Data {
		envVars = append(envVars, corev1.EnvVar{Name: env, Value: string(v)})
	}

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
	}

	volumeMounts := []corev1.VolumeMount{
		{
			Name:      strings.ToLower(instance.Kind) + "-scripts",
			ReadOnly:  true,
			MountPath: "/usr/local/bin",
		},
	}

	nodes := []string{}
	for _, node := range deletedTaggedMachineNames {
		nodes = append(nodes, node.Name)
	}
	envVars = append(envVars, corev1.EnvVar{Name: "DISABLE_COMPUTE_SERVICES", Value: strings.Join(nodes, " ")})

	// Create job that runs the disable task
	jobName := "disable-nova-compute-service-" + instance.Spec.RoleName
	containerCommand := []string{"/usr/local/bin/disablecomputeservice.sh"}

	err := util.CreateJob(c, instance, jobName, volumeMounts, volumes, containerCommand, envVars)
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	} else if errors.IsAlreadyExists(err) {
		log.Info(fmt.Sprintf("Job already present: %v", jobName))
	} else {
		log.Info(fmt.Sprintf("Job created: %v", jobName))
	}

	return nil
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

func ensureMachineSetSync(c client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack) error {
	// get replicas at the openshift-machine-api machineset
	workerMachineSet := &machinev1beta1.MachineSet{}
	machineSetName := instance.Spec.ClusterName + "-" + instance.Spec.RoleName
	err := c.Get(context.TODO(), types.NamespacedName{Name: machineSetName, Namespace: "openshift-machine-api"}, workerMachineSet)
	if err != nil && errors.IsNotFound(err) {
		log.Info(fmt.Sprintf("Machineset not found, recreate it: %s osp-worker nodes: %d", machineSetName, instance.Spec.Workers))
		if err := c.Update(context.TODO(), instance); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if *workerMachineSet.Spec.Replicas != instance.Spec.Workers {
			// MachineSet has been updated, force CRD re-sync to match the machineset replicas
			instance.Spec.Workers = *workerMachineSet.Spec.Replicas
			if err := c.Update(context.TODO(), instance); err != nil {
				return err
			}
		}
	}
	return nil
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

	updatedNode, err := kclient.CoreV1().Nodes().Update(&node)
	if err != nil || updatedNode == nil {
		return fmt.Errorf("failed to update node %v after adding taint: %v", node.Name, err)
	}

	log.Info(fmt.Sprintf("StopDaemonSetPods taint added on node %v", node.Name))
	return nil
}

func ensureComputeNodeOpenStackScriptsConfigMap(r *ReconcileComputeNodeOpenStack, instance *computenodev1alpha1.ComputeNodeOpenStack, name string) error {
	scriptsConfigMap := computenodeopenstack.ScriptsConfigMap(instance, name)

	// Check if this ScriptsConfigMap already exists
	foundScriptsConfigMap := &corev1.ConfigMap{}
	err := r.client.Get(context.TODO(), types.NamespacedName{Name: scriptsConfigMap.Name, Namespace: scriptsConfigMap.Namespace}, foundScriptsConfigMap)
	if err != nil && errors.IsNotFound(err) {
		log.Info(fmt.Sprintf("Creating a new ScriptsConfigMap - Namespace: %s, Config Map name: %s", scriptsConfigMap.Namespace, scriptsConfigMap.Name))
		if err := controllerutil.SetControllerReference(instance, scriptsConfigMap, r.scheme); err != nil {
			return err
		}

		err = r.client.Create(context.TODO(), scriptsConfigMap)
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
			err = r.client.Update(context.TODO(), foundScriptsConfigMap)
			if err != nil {
				return err
			}
			log.Info(fmt.Sprintf("ScriptsConfigMap updated - New Data Hash: %s", scriptsConfigMapDataHash))
		}
	}
	return nil
}

func getOpenStackClientInformation(r *ReconcileComputeNodeOpenStack, instance *computenodev1alpha1.ComputeNodeOpenStack) (*corev1.Secret, *corev1.ConfigMap, error) {
	openStackClientAdminSecret := instance.Spec.OpenStackClientAdminSecret
	openStackClientConfigMap := instance.Spec.OpenStackClientConfigMap

	// check for Secret with information required for scale down tasks to connect to the OpenStack API
	openstackClientAdmin := &corev1.Secret{}
	// Check if secret holding the admin information for connection to the api endpoints exist
	err := r.client.Get(context.TODO(), types.NamespacedName{Name: openStackClientAdminSecret, Namespace: instance.Namespace}, openstackClientAdmin)
	if err != nil && errors.IsNotFound(err) {
		return nil, nil, fmt.Errorf("Failed to find the secret %s holding admin user information required to connect to the OSP API endpoints: %v", openStackClientAdminSecret, err)
	}

	// check for ConfigMap with information required for scale down tasks to connect to the OpenStack API
	openstackClient := &corev1.ConfigMap{}
	// Check if configmap holding the information for connection to the api endpoints exist
	err = r.client.Get(context.TODO(), types.NamespacedName{Name: openStackClientConfigMap, Namespace: instance.Namespace}, openstackClient)
	if err != nil && errors.IsNotFound(err) {
		return nil, nil, fmt.Errorf("Failed to find the configmap %s holding information required to connect to the OSP API endpoints: %v", openStackClientConfigMap, err)
	}
	return openstackClientAdmin, openstackClient, nil
}

func getDeletedOspWorkerNodes(c client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack) ([]computenodev1alpha1.DisabledNode, error) {
	deletedOspWorkerNodes := []computenodev1alpha1.DisabledNode{}
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
			nodeName := computenodev1alpha1.DisabledNode{Name: machine.Status.NodeRef.Name}
			deletedOspWorkerNodes = append(deletedOspWorkerNodes, nodeName)
		}
	}
	return deletedOspWorkerNodes, nil
}
