package objectstorage

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
)

const (
	controllerName = "storage-objectstorage"
	cacheFinalizer = "objectstorage.stackdome.io/cleanup"
)

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

type subReconciler interface {
	reconcile(ctx context.Context, resource *storagev1alpha1.ObjectStorage) (subReconcilerResult, error)
	name() string
}

type ObjectStorageReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	ObjectStorageImage string
	subReconcilers     []subReconciler
}

func NewObjectStorageReconciler(c client.Client, scheme *runtime.Scheme, objectStorageImage string) *ObjectStorageReconciler {
	r := &ObjectStorageReconciler{
		Client:             c,
		Scheme:             scheme,
		ObjectStorageImage: objectStorageImage,
	}

	r.subReconcilers = []subReconciler{
		newVolumeReconciler(c, scheme),
		newCredentialsReconciler(c, scheme),
		newDeploymentReconciler(c, scheme, objectStorageImage),
		newServiceReconciler(c, scheme),
		newIngressReconciler(c, scheme),
		newBucketReconciler(c),
	}

	return r
}

func (r *ObjectStorageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling ObjectStorage", "name", req.Name, "namespace", req.Namespace)

	resource := &storagev1alpha1.ObjectStorage{}
	if err := r.Get(ctx, req.NamespacedName, resource); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if resource.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(resource, cacheFinalizer) {
			if err := r.reconcileDelete(ctx, resource); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(resource, cacheFinalizer)
			return ctrl.Result{}, r.Client.Update(ctx, resource)
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(resource, cacheFinalizer) {
		controllerutil.AddFinalizer(resource, cacheFinalizer)
		return ctrl.Result{}, r.Client.Update(ctx, resource)
	}

	originalStatus := resource.Status.DeepCopy()

	r.initializeStatusAndPhase(resource)

	res, err := r.reconcile(ctx, resource)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !equality.Semantic.DeepEqual(originalStatus, &resource.Status) {
		if err := r.Client.Status().Update(ctx, resource); err != nil {
			return ctrl.Result{}, err
		}
	}
	return res, nil
}

func (r *ObjectStorageReconciler) initializeStatusAndPhase(resource *storagev1alpha1.ObjectStorage) {
	resource.Status.ObservedGeneration = resource.Generation
	if resource.Status.Phase == "" {
		resource.Status.Phase = storagev1alpha1.ObjectStoragePhasePending
	}
	cond := meta.FindStatusCondition(resource.Status.Conditions, storagev1alpha1.ObjectStorageConditionAvailable)
	if cond == nil {
		meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
			Type:               storagev1alpha1.ObjectStorageConditionAvailable,
			Status:             metav1.ConditionUnknown,
			ObservedGeneration: resource.Generation,
			Reason:             "ObjectStorageStatusUnknown",
			Message:            "ObjectStorage status is unknown",
		})
	}
}

func (r *ObjectStorageReconciler) reconcile(ctx context.Context, resource *storagev1alpha1.ObjectStorage) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	for _, sub := range r.subReconcilers {
		logger.Info("running sub-reconciler", "name", sub.name())
		res, err := sub.reconcile(ctx, resource)
		if err != nil {
			logger.Error(err, "Error in sub-reconciler", "name", sub.name())
			return ctrl.Result{}, err
		}
		if res.resultNil {
			continue
		}
		if res.resultStop {
			return ctrl.Result{}, nil
		}
		if res.resultRequeue {
			return ctrl.Result{Requeue: true}, nil
		}
		if res.resultRequeueAfter != nil {
			return ctrl.Result{RequeueAfter: *res.resultRequeueAfter}, nil
		}
	}

	setStatusCondition(resource, storagev1alpha1.ObjectStorageConditionAvailable, metav1.ConditionTrue, "ObjectStorageAvailable", "ObjectStorage is available")
	setPhase(resource, storagev1alpha1.ObjectStoragePhaseReady)

	return ctrl.Result{}, nil
}

func (r *ObjectStorageReconciler) reconcileDelete(ctx context.Context, resource *storagev1alpha1.ObjectStorage) error {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling ObjectStorage deletion", "name", resource.Name, "namespace", resource.Namespace)
	return nil
}

func (r *ObjectStorageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.ObjectStorage{}).
		Owns(&storagev1alpha1.Volume{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.Secret{}).
		Owns(&networkingv1.Ingress{}).
		Named(controllerName).
		Complete(r)
}

func setStatusCondition(resource *storagev1alpha1.ObjectStorage, conditionType string, status metav1.ConditionStatus, reason, message string) {
	resource.Status.ObservedGeneration = resource.Generation
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		ObservedGeneration: resource.Generation,
		Reason:             reason,
		Message:            message,
	})
}

func setPhase(resource *storagev1alpha1.ObjectStorage, phase string) {
	resource.Status.Phase = phase
}
