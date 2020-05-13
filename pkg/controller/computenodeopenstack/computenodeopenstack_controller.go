package computenodeopenstack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	computenodev1alpha1 "github.com/openstack-k8s-operators/compute-node-operator/pkg/apis/computenode/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/openstack-k8s-operators/compute-node-operator/pkg/apply"
	"github.com/openstack-k8s-operators/compute-node-operator/pkg/render"
	"github.com/openstack-k8s-operators/compute-node-operator/pkg/util"

	machinev1beta1 "github.com/openshift/cluster-api/pkg/apis/machine/v1beta1"
)

var log = logf.Log.WithName("controller_computenodeopenstack")

// ManifestPath is the path to the manifest templates
var ManifestPath = "./bindata"

// The periodic resync interval.
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

	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &computenodev1alpha1.ComputeNodeOpenStack{},
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

	specMDS, err := util.CalculateHash(instance.Spec)
	if err != nil {
		return reconcile.Result{}, err
	}
	if reflect.DeepEqual(specMDS, instance.Status.SpecMDS) {
		err := ensureMachineSetSync(r.client, instance)
		if err != nil {
			return reconcile.Result{}, err
		}

		err = updateNodesStatus(r.client, instance)
		if err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
	}

	// Check if nodes to delete information has changed
	if !reflect.DeepEqual(instance.Spec.NodesToDelete, instance.Status.NodesToDelete) || instance.Spec.Workers < instance.Status.Workers {
		err := updateMachineDeletionSelection(r.client, instance)
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

	// Generate the objects
	objs := []*uns.Unstructured{}
	manifests, err := render.RenderDir(filepath.Join(ManifestPath, "worker-osp"), &data)
	if err != nil {
		log.Error(err, "Failed to render manifests : %v")
		return reconcile.Result{}, err
	}
	objs = append(objs, manifests...)

	// Apply the objects to the cluster
	for _, obj := range objs {
		// Set ComputeOpenStack instance as the owner and controller
		oref := metav1.NewControllerRef(instance, instance.GroupVersionKind())
		obj.SetOwnerReferences([]metav1.OwnerReference{*oref})

		// Open question: should an error here indicate we will never retry?
		if err := apply.ApplyObject(context.TODO(), r.client, obj); err != nil {
			log.Error(err, "Failed to apply objects")
			return reconcile.Result{}, err
		}
	}

	/* Only create the new ones, deleting the old ones not needed anymore
	// create node-exporter daemonset (to have monitoring information)
	// create machine-config-daemon daemonset(to allow reconfigurations)
	// create multus daemonset (to set the node to ready)
	*/
	if !reflect.DeepEqual(instance.Spec.InfraDaemonSets, instance.Status.InfraDaemonSets) {
		if err := ensureInfraDaemonsets(context.TODO(), r.kclient, instance); err != nil {
			log.Error(err, "Failed to create the infra daemon sets")
			return reconcile.Result{}, err
		}
	}

	// Update Status
	instance.Status.Workers = instance.Spec.Workers
	instance.Status.InfraDaemonSets = instance.Spec.InfraDaemonSets
	instance.Status.NodesToDelete = instance.Spec.NodesToDelete
	instance.Status.SpecMDS = specMDS
	err = r.client.Status().Update(context.TODO(), instance)
	if err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{RequeueAfter: ResyncPeriod}, nil
}

func getRenderData(ctx context.Context, client client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack) (render.RenderData, error) {
	data := render.MakeRenderData()
	data.Data["ClusterName"] = instance.Spec.ClusterName
	data.Data["WorkerOspRole"] = instance.Spec.RoleName
	data.Data["K8sServiceIp"] = instance.Spec.K8sServiceIp
	data.Data["ApiIntIp"] = instance.Spec.ApiIntIp
	data.Data["Workers"] = instance.Spec.Workers
	if instance.Spec.CorePinning == "" {
		data.Data["Pinning"] = false
	} else {
		data.Data["Pinning"] = true
		data.Data["CorePinning"] = instance.Spec.CorePinning
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

func updateNodesStatus(c client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack) error {
	nodeList := &corev1.NodeList{}
	workerLabel := "node-role.kubernetes.io/" + instance.Spec.RoleName
	listOpts := []client.ListOption{
		client.MatchingLabels{workerLabel: ""},
	}
	err := c.List(context.TODO(), nodeList, listOpts...)
	if err != nil {
		return err
	}

	var readyWorkers int32 = 0
	nodesStatus := []computenodev1alpha1.Node{}
	for _, node := range nodeList.Items {
		nodeStatus := getNodeStatus(&node)
		previousNodeStatus := getPreviousNodeStatus(node.Name, instance)

		switch previousNodeStatus {
		case "None":
			if nodeStatus == "SchedulingDisabled" {
				// Node being removed
				break
			}
			// add node to status and create blocker pod
			newNode := computenodev1alpha1.Node{Name: node.Name, Status: nodeStatus}
			nodesStatus = append(nodesStatus, newNode)
			err = createBlockerPod(c, node.Name, instance)
			if err != nil {
				return err
			}
			// if nodestatus is ready, update readyWorkers
			if nodeStatus == "Ready" {
				readyWorkers += 1
			}
		case "NotReady":
			if nodeStatus == "Ready" {
				readyWorkers += 1
				newNode := computenodev1alpha1.Node{Name: node.Name, Status: nodeStatus}
				nodesStatus = append(nodesStatus, newNode)

			} else if nodeStatus == "SchedulingDisabled" {
				err = deleteBlockerPodFinalizer(c, node.Name, instance)
			}
		case "Ready":
			newNode := computenodev1alpha1.Node{Name: node.Name, Status: nodeStatus}
			nodesStatus = append(nodesStatus, newNode)

			if nodeStatus == "Ready" {
				readyWorkers += 1
			} else if nodeStatus == "SchedulingDisabled" {
				// trigger drain (new pod)
				err = triggerNodeDrain(c, node.Name, instance)
				if err != nil {
					return err
				}
			}
		case "SchedulingDisabled":
			// if drain has finished (pod completed) remove blocker-pod finalizer
			drained, err := isNodeDrained(c, node.Name, instance)
			if err != nil {
				return err
			}
			if drained {
				err = deleteBlockerPodFinalizer(c, node.Name, instance)
				if err != nil {
					return err
				}

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
			} else {
				newNode := computenodev1alpha1.Node{Name: node.Name, Status: nodeStatus}
				nodesStatus = append(nodesStatus, newNode)
			}
		}
	}

	instance.Status.ReadyWorkers = readyWorkers
	instance.Status.Nodes = nodesStatus
	err = c.Status().Update(context.TODO(), instance)
	if err != nil {
		return err
	}
	return nil
}

func removeNode(nodes []computenodev1alpha1.NodeToDelete, i int) []computenodev1alpha1.NodeToDelete {
	nodes[i] = nodes[len(nodes)-1]
	return nodes[:len(nodes)-1]
}

func getNodeStatus(node *corev1.Node) string {
	if node.Spec.Unschedulable {
		return "SchedulingDisabled"
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == "Ready" {
			if condition.Status == "True" {
				return "Ready"
			} else {
				return "NotReady"
			}

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
	return nil
}

func triggerNodeDrain(c client.Client, nodeName string, instance *computenodev1alpha1.ComputeNodeOpenStack) error {
	// TO DO: This needs to be changed with a pod (job) that does the actual draining
	sleepTime := "infinity"

	// Example to handle drain information
	//for _, nodeToDelete := range instance.Spec.NodesToDelete {
	//	if nodeToDelete.Name == nodeName {
	//		if nodeToDelete.Drain {
	//			sleepTime = "600"
	//		}
	//		break
	//	}
	//}
	// Creating a simple pod that sleeps for 5 mins for testing purposes
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeName + "-drain-pod",
			Namespace: instance.Namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:    nodeName + "-drain-pod",
				Image:   "busybox",
				Command: []string{"/bin/sh", "-ec", "sleep " + sleepTime},
			}},
			RestartPolicy: "Never",
		},
	}
	oref := metav1.NewControllerRef(instance, instance.GroupVersionKind())
	pod.SetOwnerReferences([]metav1.OwnerReference{*oref})
	if err := c.Create(context.TODO(), pod); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func isNodeDrained(c client.Client, nodeName string, instance *computenodev1alpha1.ComputeNodeOpenStack) (bool, error) {
	podName := nodeName + "-drain-pod"
	pod := &corev1.Pod{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: podName, Namespace: instance.Namespace}, pod)
	if err != nil && errors.IsNotFound(err) {
		return true, nil
	} else if err != nil {
		return false, err
	}

	if pod.Status.Phase == "Succeeded" {
		err = c.Delete(context.TODO(), pod)
		if err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
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
	if err != nil && !errors.IsNotFound(err) {
		// MachineSet has been deleted, force recreation but with 0 replicas
		instance.Spec.Workers = 0
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
