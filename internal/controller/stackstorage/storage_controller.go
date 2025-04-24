package stackstorage

import (
	"context"
	"fmt"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

const (
	DefaultRequeueTime    = time.Second * 5
	ownerKey              = ".metadata.controller"
	StorageControllerName = "storage-controller"
)

type subReconciler interface {
	reconcile(context.Context, *storagev1alpha1.Storage) (subReconcilerResult, error)
}

type StorageReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	subReconcilers []subReconciler
}

type subReconcilerResult struct {
	resultNil          bool
	resultStop         bool
	resultRequeue      bool
	resultRequeueAfter *time.Duration
}

var (
	resultNil          = subReconcilerResult{resultNil: true}
	resultStop         = subReconcilerResult{resultStop: true}
	resultRequeue      = subReconcilerResult{resultRequeue: true}
	resultRequeueAfter = func(t time.Duration) subReconcilerResult {
		return subReconcilerResult{resultRequeueAfter: &t}
	}
)

func (r *StorageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger = logger.WithValues("stackstorage", req.NamespacedName.String())
	ctx = controller.ContextWithLogger(ctx, logger)
	storage := &storagev1alpha1.Storage{}

	err := r.Client.Get(ctx, req.NamespacedName, storage)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	reconcileRes, err := r.reconcile(ctx, storage)
	if err != nil {
		return ctrl.Result{}, err
	}
	return reconcileRes, r.Client.Status().Update(ctx, storage)
}

func (r *StorageReconciler) reconcile(ctx context.Context, storage *storagev1alpha1.Storage) (ctrl.Result, error) {
	// Assume storageclass already present
	// TODO: Automate this too
	for _, reconciler := range r.subReconcilers {
		subReconcilerRes, err := reconciler.reconcile(ctx, storage)
		if err != nil {
			return ctrl.Result{}, err
		}
		switch {
		case subReconcilerRes == resultStop:
			return ctrl.Result{}, nil
		case subReconcilerRes == resultRequeue:
			return ctrl.Result{Requeue: true}, nil
		case subReconcilerRes.resultRequeueAfter != nil:
			return ctrl.Result{RequeueAfter: *subReconcilerRes.resultRequeueAfter}, nil
		}
	}

	return ctrl.Result{}, nil
}

func NewStackStorageReconciler(client client.Client, uncachedClient client.Client, scheme *runtime.Scheme) *StorageReconciler {
	subReconcilers := []subReconciler{
		&volumeReconciler{
			Client: client,
			Scheme: scheme,
		},
		&userSShKeySecretReconciler{
			Client:         client,
			UncachedClient: uncachedClient,
			Scheme:         scheme,
		},
		&sshServerReconciler{
			Client: client,
			Scheme: scheme,
		},
		&serviceReconciler{
			Client: client,
			Scheme: scheme,
		},
	}
	return &StorageReconciler{
		Client:         client,
		Scheme:         scheme,
		subReconcilers: subReconcilers,
	}
}

func reportStorageUnAvailable(storage *storagev1alpha1.Storage, reason string, msg string, msgArgs ...any) {
	if storage.Labels == nil {
		storage.Labels = make(map[string]string)
	}
	objectStackdomeServerVersion, ok := storage.Labels[v1alpha1.StackdomeObjectGeneration]
	if ok {
		generation, _ := strconv.ParseInt(objectStackdomeServerVersion, 10, 64)
		storage.Status.ObservedStackdomeServerObjectGeneration = generation
	}
	storage.Status.Phase = storagev1alpha1.StoragePending
	meta.SetStatusCondition(&storage.Status.Conditions, metav1.Condition{
		Type:               string(storagev1alpha1.StorageAvailable),
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            fmt.Sprintf(msg, msgArgs...),
		ObservedGeneration: storage.Generation,
	})
	storage.Status.StatusHash = storage.StatusHash()
}

func reportStorageAvailable(storage *storagev1alpha1.Storage, storageSvc *corev1.Service) {
	if storage.Labels == nil {
		storage.Labels = make(map[string]string)
	}
	objectStackdomeServerVersion, ok := storage.Labels[v1alpha1.StackdomeObjectGeneration]
	if ok {
		generation, _ := strconv.ParseInt(objectStackdomeServerVersion, 10, 64)
		storage.Status.ObservedStackdomeServerObjectGeneration = generation
	}
	storage.Status.Phase = storagev1alpha1.StorageReady
	storage.Status.ServiceName = storageSvc.Name
	meta.SetStatusCondition(&storage.Status.Conditions, metav1.Condition{
		Type:               string(storagev1alpha1.StorageAvailable),
		Status:             metav1.ConditionTrue,
		Reason:             "AllComponentsUp",
		Message:            "All components are up and running.",
		ObservedGeneration: storage.Generation,
	})
	storage.Status.StatusHash = storage.StatusHash()
}

// SetupWithManager sets up the controller with the Manager.
func (r *StorageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &storagev1alpha1.Volume{}, ownerKey, func(rawObj client.Object) []string {
		wsVolume := rawObj.(*storagev1alpha1.Volume)
		owner := metav1.GetControllerOf(wsVolume)
		return []string{owner.Name}
	}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.Storage{}).
		Watches(&corev1.Service{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &storagev1alpha1.Storage{})).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &storagev1alpha1.Storage{})).
		Watches(&storagev1alpha1.Volume{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &storagev1alpha1.Storage{})).
		Complete(r)
}
