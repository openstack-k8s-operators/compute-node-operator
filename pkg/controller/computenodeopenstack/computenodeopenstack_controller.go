package computenodeopenstack

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	computenodev1alpha1 "github.com/openstack-k8s-operators/compute-node-operator/pkg/apis/computenode/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
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

	"github.com/openstack-k8s-operators/compute-node-operator/pkg/render"
	"github.com/openstack-k8s-operators/compute-node-operator/pkg/apply"

	machinev1beta1 "github.com/openshift/cluster-api/pkg/apis/machine/v1beta1"
)

var log = logf.Log.WithName("controller_computenodeopenstack")

// ManifestPath is the path to the manifest templates
var ManifestPath = "./bindata"

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

	// TODO(user): Modify this to be the types you create that are owned by the primary resource
	// Watch for changes to secondary resource Pods and requeue the owner ComputeNodeOpenStack
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
	client client.Client
	scheme *runtime.Scheme
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

	// create node-exporter daemonset (to have monitoring information)
	// create machine-config-daemon daemonset(to allow reconfigurations)
	// create multus daemonset (to set the node to ready)
	if err := createInfraDaemonsets(context.TODO(), r.kclient, instance); err != nil {
		log.Error(err, "Failed to create the infra daemon sets")
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}


func getRenderData(ctx context.Context, client client.Client, instance *computenodev1alpha1.ComputeNodeOpenStack) (render.RenderData, error)  {
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

func createInfraDaemonsets(ctx context.Context, client kubernetes.Interface, instance *computenodev1alpha1.ComputeNodeOpenStack) error {
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
				if !equality.Semantic.DeepEqual(ospDaemonSet.Spec, ds.Spec) {
					//if err := client.Update(ctx, ds); err != nil {
					_, err := client.AppsV1().DaemonSets(dsInfo.Namespace).Update(ds)
					if err != nil {
						log.Error(err, "could not update object", ospDaemonSetName)
						return err
					}
				}
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
		Effect: "NoSchedule",
		Key: "dedicated",
		Value: instance.Spec.RoleName,
	}
	daemonSet.Spec.Template.Spec.Tolerations = append(daemonSet.Spec.Template.Spec.Tolerations, tolerationSpec)

	// Change nodeSelector
	nodeSelector := "node-role.kubernetes.io/" + instance.Spec.RoleName
	daemonSet.Spec.Template.Spec.NodeSelector = map[string]string{nodeSelector: ""}

	return &daemonSet
}