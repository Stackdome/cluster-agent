package clusterinfo

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/davecgh/go-spew/spew"
	corev1 "k8s.io/api/core/v1"
	networkv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	k8sapierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

type ClusterInfoReconciler struct {
	client.Client
	UncachedClient  client.Client
	Scheme          *runtime.Scheme
	DiscoveryClient discovery.DiscoveryInterface
}

func (r *ClusterInfoReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Client = mgr.GetClient()
	r.Scheme = mgr.GetScheme()

	uncached, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		return fmt.Errorf("failed to create uncached client: %w", err)
	}
	r.UncachedClient = uncached

	dc, err := discovery.NewDiscoveryClientForConfig(mgr.GetConfig())
	if err != nil {
		return fmt.Errorf("failed to create discovery client: %w", err)
	}
	r.DiscoveryClient = dc

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1alpha1.ClusterInfo{}).
		WatchesRawSource(source.Func(enqueueClusterInfoSingleton)).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(mapToSingleton)).
		Watches(&storagev1.StorageClass{}, handler.EnqueueRequestsFromMapFunc(mapToSingleton)).
		Watches(&corev1.Service{}, handler.EnqueueRequestsFromMapFunc(mapLBServiceToSingleton)).
		Watches(&networkv1.IngressClass{}, handler.EnqueueRequestsFromMapFunc(mapToSingleton)).
		Complete(r)
}

func enqueueClusterInfoSingleton(_ context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
	q.Add(reconcile.Request{
		NamespacedName: types.NamespacedName{Name: corev1alpha1.ClusterInfoSingletonName},
	})
	return nil
}

func mapToSingleton(_ context.Context, _ client.Object) []reconcile.Request {
	return []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: corev1alpha1.ClusterInfoSingletonName}},
	}
}

func mapLBServiceToSingleton(_ context.Context, obj client.Object) []reconcile.Request {
	svc, ok := obj.(*corev1.Service)
	if !ok || svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
		return nil
	}
	return []reconcile.Request{
		{NamespacedName: types.NamespacedName{Name: corev1alpha1.ClusterInfoSingletonName}},
	}
}

func (r *ClusterInfoReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cr := &corev1alpha1.ClusterInfo{}
	if err := r.UncachedClient.Get(ctx, req.NamespacedName, cr); err != nil {
		if k8sapierrors.IsNotFound(err) {
			return ctrl.Result{Requeue: true}, r.Create(ctx, &corev1alpha1.ClusterInfo{
				ObjectMeta: metav1.ObjectMeta{Name: corev1alpha1.ClusterInfoSingletonName},
				Spec: corev1alpha1.ClusterInfoSpec{
					LoadBalancerNamespaces: []string{corev1alpha1.ClusterInfoDefaultLBNamespace},
				},
			})
		}
		return ctrl.Result{}, err
	}

	status, err := r.collectStatus(ctx, cr.Spec)
	if err != nil {
		return ctrl.Result{}, err
	}

	hash := computeStatusHash(status)
	if hash == cr.Status.StatusHash {
		return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
	}

	now := metav1.Now()
	status.StatusHash = hash
	status.LastRefreshedAt = &now
	status.Phase = corev1alpha1.ClusterInfoPhaseReady

	patch := client.MergeFrom(cr.DeepCopy())
	cr.Status = *status
	if err := r.Status().Patch(ctx, cr, patch); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

func (r *ClusterInfoReconciler) collectStatus(ctx context.Context, spec corev1alpha1.ClusterInfoSpec) (*corev1alpha1.ClusterInfoStatus, error) {
	status := &corev1alpha1.ClusterInfoStatus{}

	if r.DiscoveryClient != nil {
		if version, err := r.DiscoveryClient.ServerVersion(); err == nil {
			status.KubernetesVersion = version.GitVersion
		}
	}

	nodeList := &corev1.NodeList{}
	if err := r.UncachedClient.List(ctx, nodeList); err != nil {
		return nil, err
	}
	status.Nodes, status.AvailabilityZones = BuildNodeInfoList(nodeList.Items)
	status.TotalNodes = len(status.Nodes)
	for _, n := range status.Nodes {
		if n.Ready {
			status.ReadyNodes++
		}
	}

	scList := &storagev1.StorageClassList{}
	if err := r.UncachedClient.List(ctx, scList); err != nil {
		return nil, err
	}
	status.StorageClasses = BuildStorageClassInfoList(scList.Items)

	var allSvcs []corev1.Service
	for _, ns := range spec.LoadBalancerNamespaces {
		svcList := &corev1.ServiceList{}
		if err := r.UncachedClient.List(ctx, svcList, client.InNamespace(ns)); err != nil {
			return nil, err
		}
		allSvcs = append(allSvcs, svcList.Items...)
	}
	status.LoadBalancers = BuildLoadBalancerInfoList(allSvcs)

	icList := &networkv1.IngressClassList{}
	if err := r.UncachedClient.List(ctx, icList); err != nil {
		return nil, err
	}
	status.IngressClasses = BuildIngressClassInfoList(icList.Items)

	return status, nil
}

func computeStatusHash(status *corev1alpha1.ClusterInfoStatus) string {
	hasher := fnv.New32a()
	hasher.Reset()
	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	saved := status.StatusHash
	status.StatusHash = ""
	printer.Fprintf(hasher, "%#v", status)
	status.StatusHash = saved
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum32()))
}
