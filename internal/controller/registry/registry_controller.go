package registry

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
	reg "stackdome.io/cluster-agent/pkg/registry"
)

const (
	registryNamespace  = "stackdome-registry"
	cacheFinalizer     = "registry.stackdome.io/cache"
	registryController = "ClusterRegistryController"
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

type RegistryReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	registryBuilder reg.RegistryBuilder
	subReconcilers  map[string]subReconciler
}

type subReconciler func(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (subReconcilerResult, error)

func NewRegistryReconciler(client client.Client, scheme *runtime.Scheme, registryBuilder reg.RegistryBuilder) *RegistryReconciler {
	r := &RegistryReconciler{
		Client:          client,
		Scheme:          scheme,
		registryBuilder: registryBuilder,
	}

	utilruntime.Must(registryBuilder.Initialize(reg.RegistryBuilderOpts{
		Client:           client,
		StorageDirectory: "/var/lib/registry",
		ConfigPath:       "/etc/registry/config.json",
		Auth: reg.AuthOpts{
			Htpasswd: reg.HtpasswdOpts{
				Path: "/etc/auth/htpasswd",
			},
		},
		Namespace: registryNamespace,
	}))

	r.subReconcilers = map[string]subReconciler{
		"NamepspaceReonciler":          r.reconcileRegistryNamespace,
		"RegistryAuthReconciler":       r.reconcileRegistryAuth,
		"RegistryStorageReconciler":    r.reconcileRegistryStorage,
		"RegistryConfigReconciler":     r.reconcileRegistryConfig,
		"RegistryDeploymentReconciler": r.reconcileRegistryDeployment,
		"RegistryServiceReconciler":    r.reconcileRegistryService,
	}
	return r
}

func (r *RegistryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ctx = controller.ContextWithLogger(ctx, logger)
	registry := &registryv1alpha1.ClusterRegistry{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: req.Name}, registry); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if registry.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(registry, cacheFinalizer) {
			if err := r.reconcileDelete(ctx, registry); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(registry, cacheFinalizer)
			return ctrl.Result{}, r.Client.Update(ctx, registry)
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(registry, cacheFinalizer) {
		controllerutil.AddFinalizer(registry, cacheFinalizer)
		return ctrl.Result{}, r.Client.Update(ctx, registry)
	}

	result, err := r.reconcile(ctx, registry)
	if err != nil {
		return ctrl.Result{}, err
	}

	return result, r.Client.Status().Update(ctx, registry)
}

func (r *RegistryReconciler) reconcileDelete(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) error {
	meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{
		Type:               string(registryv1alpha1.RegistryReady),
		Status:             metav1.ConditionFalse,
		Reason:             "RegistryDeletion",
		Message:            "Registry is being deleted",
		ObservedGeneration: registry.Generation,
	})
	registry.Status.InternalURL = ""
	registry.Status.Phase = registryv1alpha1.RegistryPhasePending
	registry.Status.ObservedGeneration = registry.Generation
	return nil
}

func (r *RegistryReconciler) reconcile(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (ctrl.Result, error) {
	for reconcilerName, subReconciler := range r.subReconcilers {
		logger := log.FromContext(ctx)
		logger.Info("reconciling", "sub-reconciler", reconcilerName)
		result, err := subReconciler(ctx, registry)
		switch {
		case err != nil:
			logger.Error(err, "failed to reconcile", "sub-reconciler", reconcilerName)
			return ctrl.Result{}, err
		case result.resultStop:
			return ctrl.Result{}, nil
		case result.resultRequeue:
			return ctrl.Result{Requeue: true}, nil
		case result.resultRequeueAfter != nil:
			return ctrl.Result{RequeueAfter: *result.resultRequeueAfter}, nil
		}
	}
	return ctrl.Result{}, nil
}

// Reconcile registry namespace
func (r *RegistryReconciler) reconcileRegistryNamespace(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (subReconcilerResult, error) {
	desiredNamespace := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: registryNamespace},
		Spec:       corev1.NamespaceSpec{Finalizers: []corev1.FinalizerName{cacheFinalizer}},
	}
	existingNamespace := &corev1.Namespace{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&desiredNamespace), existingNamespace); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Client.Create(ctx, &desiredNamespace)
		}
		return resultNil, err
	}
	return resultNil, nil
}

func (r *RegistryReconciler) reconcileRegistryDeployment(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (subReconcilerResult, error) {
	logger := log.FromContext(ctx)
	desiredDeployment, err := r.registryBuilder.BuildDeployment(ctx, registry)
	if err != nil {
		logger.Error(err, "failed to build registry deployment")
		return resultNil, err
	}
	if err := controllerutil.SetControllerReference(registry, desiredDeployment, r.Scheme); err != nil {
		return resultNil, err
	}

	existingDeployment := &appsv1.Deployment{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(desiredDeployment), existingDeployment); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Client.Create(ctx, desiredDeployment)
		}
		return resultNil, err
	}
	if err := r.Client.Patch(ctx, desiredDeployment, client.Apply, &client.PatchOptions{
		Force:        ptr.To(true),
		FieldManager: registryController,
	}); err != nil {
		return resultNil, err
	}

	// Check if the deployment is ready
	if controller.DeploymentAvailable(existingDeployment) {
		return resultNil, nil
	}

	meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{
		Type:               string(registryv1alpha1.RegistryReady),
		Status:             metav1.ConditionFalse,
		Reason:             "RegistryDeploymentNotReady",
		Message:            "RegistryDeployment is not Available",
		ObservedGeneration: registry.Generation,
	})
	registry.Status.InternalURL = ""
	registry.Status.Phase = registryv1alpha1.RegistryPhasePending
	registry.Status.ObservedGeneration = registry.Generation

	// We will get requeued when deployment is ready.
	return resultStop, nil
}

// reconcile svc for the registry
func (r *RegistryReconciler) reconcileRegistryService(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (subReconcilerResult, error) {
	desiredService, registryURL, err := r.registryBuilder.BuildService(ctx, registry)
	if err != nil {
		return resultNil, err
	}

	if err := controllerutil.SetOwnerReference(registry, desiredService, r.Scheme); err != nil {
		return resultNil, err
	}

	existingService := &corev1.Service{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(desiredService), existingService); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Client.Create(ctx, desiredService)
		}
		return resultNil, err
	}

	if err := r.Client.Patch(ctx, desiredService, client.Apply, &client.PatchOptions{
		Force:        ptr.To(true),
		FieldManager: registryController,
	}); err != nil {
		return resultNil, err
	}

	meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{
		Type:               string(registryv1alpha1.RegistryReady),
		Status:             metav1.ConditionTrue,
		Reason:             "RegistryDeploymentAndServiceAvailable",
		Message:            "Registry Deployment and Service are Available",
		ObservedGeneration: registry.Generation,
	})
	registry.Status.Phase = registryv1alpha1.RegistryPhaseRunning
	registry.Status.ObservedGeneration = registry.Generation
	registry.Status.InternalURL = registryURL
	return resultNil, nil
}

func (r *RegistryReconciler) reconcileRegistryStorage(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (subReconcilerResult, error) {
	resourceSize, err := k8sresource.ParseQuantity(registry.Spec.Storage.Size)
	if err != nil {
		meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{
			Type:               string(registryv1alpha1.RegistryReady),
			Status:             metav1.ConditionFalse,
			Reason:             "RegistryStorageSizeParseError",
			Message:            fmt.Sprintf("Failed to parse resource size in the resource: %v", err),
			ObservedGeneration: registry.Generation,
		})
		registry.Status.InternalURL = ""
		registry.Status.Phase = registryv1alpha1.RegistryPhasePending
		registry.Status.ObservedGeneration = registry.Generation
		return resultStop, nil
	}

	desiredPVC := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      registry.RegistryPVCName(),
			Namespace: registryNamespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resourceSize},
			},
			StorageClassName: registry.Spec.Storage.StorageClass,
		},
	}
	if err := controllerutil.SetOwnerReference(registry, &desiredPVC, r.Scheme); err != nil {
		return resultNil, err
	}

	existingPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(&desiredPVC), existingPVC); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Client.Create(ctx, &desiredPVC)
		}
		return resultNil, err
	}

	// TODO: Support volume expansion.
	return resultNil, nil
}

// Reconcile authentications for the registry
func (r *RegistryReconciler) reconcileRegistryAuth(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (subReconcilerResult, error) {
	if registry.Spec.Auth.HtPasswordCredentials != nil {
		return r.reconcileHtPasswordAuthSecret(ctx, registry)
	}
	return resultNil, nil
}

func (r *RegistryReconciler) reconcileHtPasswordAuthSecret(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (subReconcilerResult, error) {
	htpasswdSecret := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      registry.Spec.Auth.HtPasswordCredentials.PasswordSecretRef.Name,
		Namespace: registry.Spec.Auth.HtPasswordCredentials.PasswordSecretRef.Namespace,
	}, htpasswdSecret); err != nil {
		if apierrors.IsNotFound(err) {
			// set status condition
			meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{
				Type:               string(registryv1alpha1.RegistryReady),
				Status:             metav1.ConditionFalse,
				Reason:             "HtPasswordAuthSecretNotFound",
				Message:            "HtPasswordAuthSecret not found",
				ObservedGeneration: registry.Generation,
			})
			registry.Status.InternalURL = ""
			registry.Status.Phase = registryv1alpha1.RegistryPhasePending
			registry.Status.ObservedGeneration = registry.Generation
			return resultStop, nil
		}
		return resultNil, err
	}

	password := string(htpasswdSecret.Data["password"])
	if len(password) == 0 {
		// set status condition
		meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{
			Type:               string(registryv1alpha1.RegistryReady),
			Status:             metav1.ConditionFalse,
			Reason:             "HtPasswordAuthSecretEmpty",
			Message:            "HtPasswordAuthSecret is empty",
			ObservedGeneration: registry.Generation,
		})
		registry.Status.InternalURL = ""
		registry.Status.Phase = registryv1alpha1.RegistryPhasePending
		registry.Status.ObservedGeneration = registry.Generation
		return resultStop, nil
	}
	desiredSecret, secretKey, err := r.registryBuilder.BuildHTPasswordSecret(ctx, registry, password)
	if err != nil {
		return resultNil, err
	}
	controllerutil.SetOwnerReference(registry, desiredSecret, r.Scheme)
	existingSecret := &corev1.Secret{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(desiredSecret), existingSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Client.Create(ctx, desiredSecret)
		}
		return resultNil, err
	}

	if desiredSecret.StringData[secretKey] != existingSecret.StringData[secretKey] {
		existingSecret.StringData = desiredSecret.StringData
		if err := r.Client.Update(ctx, existingSecret); err != nil {
			return resultNil, err
		}
	}

	return resultNil, nil
}

func (r *RegistryReconciler) reconcileRegistryConfig(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (subReconcilerResult, error) {
	configCM, err := r.registryBuilder.BuildConfigurationConfigMap(ctx, registry)
	if err != nil {
		return resultNil, err
	}
	if err := controllerutil.SetOwnerReference(registry, configCM, r.Scheme); err != nil {
		return resultNil, err
	}

	existingConfigMap := &corev1.ConfigMap{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(configCM), existingConfigMap); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Client.Create(ctx, configCM)
		}
		return resultNil, err
	}
	if err := r.Client.Patch(ctx, configCM, client.Apply, &client.PatchOptions{
		Force:        ptr.To(true),
		FieldManager: registryController,
	}); err != nil {
		return resultNil, err
	}
	return resultNil, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RegistryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&registryv1alpha1.ClusterRegistry{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&appsv1.Deployment{}).
		Named("registry-controller").
		Complete(r)
}
