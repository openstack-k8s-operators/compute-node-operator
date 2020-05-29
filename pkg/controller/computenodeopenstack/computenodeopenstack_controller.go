package computenodeopenstack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	computenodev1alpha1 "github.com/openstack-k8s-operators/compute-node-operator/pkg/apis/computenode/v1alpha1"
	computenodeopenstack "github.com/openstack-k8s-operators/compute-node-operator/pkg/computenodeopenstack"
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

	// openStackClientAdminSecret and openStackClientConfigMap are expected to exist in
	// openStackNamespace namespace. Default "openstack"
	// mschuppert - note: 	the format of the secret/configmap might change when the OSP controller
	//			operators who in the end should populate this information are done. So
	//			likely changes are expected.
	openStackNamespace := "openstack"
	if instance.Spec.OpenStackNamespace != "" {
		openStackNamespace = instance.Spec.OpenStackNamespace
	}
	openStackClientAdmin, openStackClient, err := getOpenStackClientInformation(r.kclient, instance, openStackNamespace)
	if err != nil {
		return reconcile.Result{}, err
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
		err = updateNodesStatus(r.client, r.kclient, instance, openStackClientAdmin, openStackClient)
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
		reqLogger.Info("Created InfraDaemonSets")
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

func updateNodesStatus(c client.Client, kclient kubernetes.Interface, instance *computenodev1alpha1.ComputeNodeOpenStack, openstackClientAdmin *corev1.Secret, openstackClient *corev1.ConfigMap) error {

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
		log.Info(fmt.Sprintf("Update node status for node: %s", node.Name))
		nodeStatus := getNodeStatus(&node)
		previousNodeStatus := getPreviousNodeStatus(node.Name, instance)

		switch previousNodeStatus {
		case "None":
			log.Info(fmt.Sprintf("Previous status None: %s", node.Name))
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
				log.Info(fmt.Sprintf("Compute worker node added: %s", node.Name))
			}
		case "NotReady":
			log.Info(fmt.Sprintf("Previous status NotReady: %s", node.Name))
			if nodeStatus == "Ready" {
				readyWorkers += 1
				newNode := computenodev1alpha1.Node{Name: node.Name, Status: nodeStatus}
				nodesStatus = append(nodesStatus, newNode)

			} else if nodeStatus == "SchedulingDisabled" {
				err = deleteBlockerPodFinalizer(c, node.Name, instance)
			}
		case "Ready":
			log.Info(fmt.Sprintf("Previous status Ready: %s", node.Name))
			newNode := computenodev1alpha1.Node{Name: node.Name, Status: nodeStatus}
			nodesStatus = append(nodesStatus, newNode)

			if nodeStatus == "Ready" {
				readyWorkers += 1
			}
		case "SchedulingDisabled":
			log.Info(fmt.Sprintf("Previous status SchedulingDisabled: %s", node.Name))
			// trigger node drain pre tasks (new pod)
			finalizerDeleted, err := isBlockerPodFinalizerDeleted(c, node.Name, instance)
			if err != nil {
				return err
			} else if finalizerDeleted {
				return nil
			}

			err = triggerNodePreDrain(c, node.Name, instance, openstackClientAdmin, openstackClient)
			if err != nil {
				return err
			}

			// if pre draining has finished (job completed) remove blocker-pod finalizer
			drainedPre, err := isNodeDrained(c, node.Name, node.Name+"-drain-job-pre", instance)
			if err != nil && errors.IsNotFound(err) {
				return fmt.Errorf("Pre NodeDrain job IsNotFound: %s", node.Name+"-drain-job-pre")
			} else if err != nil {
				return err
			}

			log.Info(fmt.Sprintf("Pre draining job: %s", drainedPre))
			switch drainedPre {
			case "succeeded":
				// taint node that daemonset pods get stopped
				// since we loop over the nodes in status update we need to
				// 1) taint the node to have OSP pods stopped
				// 2) run post drain job
				// 3) remove blocker pod to get node finally removed
				err = addToBeRemovedTaint(kclient, node)
				if err != nil {
					return err
				}

				// trigger node drain post tasks (new pod)
				err = triggerNodePostDrain(c, node.Name, instance, openstackClientAdmin, openstackClient)
				if err != nil {
					return err
				}

				// if post draining has finished (job completed) remove the pod
				drainedPost, err := isNodeDrained(c, node.Name, node.Name+"-drain-job-post", instance)
				if err != nil && errors.IsNotFound(err) {
					return fmt.Errorf("Post NodeDrain job IsNotFound: %s", node.Name+"-drain-job-post")
				} else if err != nil {
					return err
				}
				log.Info(fmt.Sprintf("NodeDrainPost status: %v ", drainedPost))
				switch drainedPost {
				case "succeeded":
					log.Info(fmt.Sprintf("NodeDrainPost job succeeded: %s", node.Name+"-drain-job-post"))
					err = deleteJobWithName(c, kclient, instance, node.Name+"-drain-job-post")
					if err != nil && !errors.IsNotFound(err) {
						return err
					}
					err = deleteJobWithName(c, kclient, instance, node.Name+"-drain-job-pre")
					if err != nil && !errors.IsNotFound(err) {
						return err
					}

					// delete blocker pod to proceed with node removal from OCP
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
				default:
					// return reconcile
					return nil
				}
			case "active":
				// return reconcile
				log.Info(fmt.Sprintf("NodeDrainPre job active: %s", node.Name+"-drain-job-pre"))
				return nil
			default:
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

func triggerNodePreDrain(c client.Client, nodeName string, instance *computenodev1alpha1.ComputeNodeOpenStack, openstackClientAdmin *corev1.Secret, openstackClient *corev1.ConfigMap) error {
	err := _triggerNodeDrain(c, nodeName, instance, true, openstackClientAdmin, openstackClient)
	if err != nil {
		return err
	}
	return nil
}

func triggerNodePostDrain(c client.Client, nodeName string, instance *computenodev1alpha1.ComputeNodeOpenStack, openstackClientAdmin *corev1.Secret, openstackClient *corev1.ConfigMap) error {
	err := _triggerNodeDrain(c, nodeName, instance, false, openstackClientAdmin, openstackClient)
	if err != nil {
		return err
	}
	return nil
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

func isNodeDrained(c client.Client, nodeName string, jobName string, instance *computenodev1alpha1.ComputeNodeOpenStack) (string, error) {
	job := &batchv1.Job{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: jobName, Namespace: instance.Namespace}, job)
	if err != nil {
		return "", err
	}

	if job.Status.Succeeded == 1 {
		log.Info(fmt.Sprintf("NodeDrain job suceeded: %s", job.Name))
		return "succeeded", nil
	} else if job.Status.Active >= 1 {
		log.Info(fmt.Sprintf("NodeDrain job still running: %s", job.Name))
		return "active", nil
	}

	// if job is not succeeded or active, return type and reason as error from first conditions
	// to log and reconcile
	conditionsType := ""
	conditionsReason := ""
	if len(job.Status.Conditions) > 0 {
		conditionsType = string(job.Status.Conditions[0].Type)
		conditionsReason = job.Status.Conditions[0].Reason
	}

	return "", fmt.Errorf("nodeDrain job type %v, reason: %v", conditionsType, conditionsReason)
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

func deleteJobWithName(c client.Client, kclient kubernetes.Interface, instance *computenodev1alpha1.ComputeNodeOpenStack, jobName string) error {
	job := &batchv1.Job{}
	err := c.Get(context.TODO(), types.NamespacedName{Name: jobName, Namespace: instance.Namespace}, job)
	if err != nil {
		return err
	}

	if job.Status.Succeeded == 1 {
		background := metav1.DeletePropagationBackground
		err = kclient.BatchV1().Jobs(instance.Namespace).Delete(jobName, &metav1.DeleteOptions{PropagationPolicy: &background})
		if err != nil {
			return fmt.Errorf("failed to delete drain job: %v", job.Name, err)
		}
		log.Info("Deleted NodeDrain job: " + job.Name)
	}

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

func getOpenStackClientInformation(kclient kubernetes.Interface, instance *computenodev1alpha1.ComputeNodeOpenStack, namespace string) (*corev1.Secret, *corev1.ConfigMap, error) {
	openStackClientAdminSecret := instance.Spec.OpenStackClientAdminSecret
	openStackClientConfigMap := instance.Spec.OpenStackClientConfigMap

	// check for Secret with information required for scale down tasks to connect to the OpenStack API
	openstackClientAdmin := &corev1.Secret{}
	// Check if secret holding the admin information for connection to the api endpoints exist
	openstackClientAdmin, err := kclient.CoreV1().Secrets(namespace).Get(openStackClientAdminSecret, metav1.GetOptions{})
	if err != nil && errors.IsNotFound(err) {
		return nil, nil, fmt.Errorf("Failed to find the secret holding admin user information required to connect to the OSP API endpoints: %v", err)
	}

	// check for ConfigMap with information required for scale down tasks to connect to the OpenStack API
	openstackClient := &corev1.ConfigMap{}
	// Check if configmap holding the information for connection to the api endpoints exist
	openstackClient, err = kclient.CoreV1().ConfigMaps(namespace).Get(openStackClientConfigMap, metav1.GetOptions{})
	if err != nil && errors.IsNotFound(err) {
		return nil, nil, fmt.Errorf("Failed to find the configmap holding information required to connect to the OSP API endpoints: %v", err)
	}
	return openstackClientAdmin, openstackClient, nil
}
