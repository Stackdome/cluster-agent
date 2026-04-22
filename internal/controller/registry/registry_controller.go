package registry

import (
	"context"
	"encoding/json"
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
	internaltypes "stackdome.io/cluster-agent/internal/types"

	reg "stackdome.io/cluster-agent/pkg/registry"
)

const (
	registryNamespace               = "stackdome-registry"
	cacheFinalizer                  = "registry.stackdome.io/cache"
	registryController              = "ClusterRegistryController"
	nodeRegistryAccessConfigMapName = "stackdome-insecure-registries"
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
	subReconcilers  []namedSubReconciler
}

type subReconciler func(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (subReconcilerResult, error)

type namedSubReconciler struct {
	name      string
	reconcile subReconciler
}

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

	r.subReconcilers = []namedSubReconciler{
		{"NamespaceReconciler", r.reconcileRegistryNamespace},
		{"RegistryAuthReconciler", r.reconcileRegistryAuth},
		{"RegistryStorageReconciler", r.reconcileRegistryStorage},
		{"RegistryConfigReconciler", r.reconcileRegistryConfig},
		{"RegistryDeploymentReconciler", r.reconcileRegistryDeployment},
		{"RegistryServiceReconciler", r.reconcileRegistryService},
		{"NodeRegistryAccessConfigMapReconciler", r.reconcileNodeRegistryAccessConfigMap},
		{"SharedRegistryConfigDaemonSetReconciler", r.reconcileSharedRegistryConfigDaemonSet},
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

func (r *RegistryReconciler) reconcileNodeRegistryAccessConfigMap(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (subReconcilerResult, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling node registry access configuration")

	if len(registry.Status.InternalURL) == 0 || registry.Status.ServiceIP == "" {
		return resultNil, nil
	}

	existingConfigMap := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      nodeRegistryAccessConfigMapName,
		Namespace: registryNamespace,
	}, existingConfigMap)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.createRegistryConfigCM(ctx, registry)
		}
		return resultNil, fmt.Errorf("failed to get ConfigMap: %w", err)
	}

	// Update existing ConfigMap by adding/updating this registry's configuration
	existingRegistryConfig, err := unmarshalRegistryConfig(existingConfigMap.Data["registries.json"])
	if err != nil {
		return resultNil, err
	}

	if existingRegistryConfig.AddRegistry(registry.Status.ServiceIP, registry.Status.InternalURL) {
		existingRegistryConfigJson, err := marshalRegistryConfig(existingRegistryConfig)
		if err != nil {
			return resultNil, err
		}
		existingConfigMap.Data["registries.json"] = existingRegistryConfigJson
		return resultNil, r.Client.Update(ctx, existingConfigMap)
	}
	return resultNil, nil
}

func unmarshalRegistryConfig(registryInfoJson string) (*internaltypes.RegistryConfig, error) {
	var res internaltypes.RegistryConfig
	if err := json.Unmarshal([]byte(registryInfoJson), &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (r *RegistryReconciler) createRegistryConfigCM(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (subReconcilerResult, error) {
	registryConfig := internaltypes.NewRegistryConfig()
	registryConfig.AddRegistry(registry.Status.ServiceIP, registry.Status.InternalURL)

	registryConfigJson, err := marshalRegistryConfig(registryConfig)
	if err != nil {
		return resultNil, err
	}

	desiredCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeRegistryAccessConfigMapName,
			Namespace: registryNamespace,
		},
		Data: map[string]string{
			"registries.json": registryConfigJson,
		},
	}
	// We dont add owner refs as the CM is shared accross all cluster registry objects.

	return resultNil, r.Create(ctx, desiredCM)
}

func marshalRegistryConfig(registryConfig *internaltypes.RegistryConfig) (string, error) {
	resBytes, err := json.Marshal(registryConfig)
	if err != nil {
		return "", err
	}
	return string(resBytes), nil
}

func (r *RegistryReconciler) reconcileSharedRegistryConfigDaemonSet(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (subReconcilerResult, error) {
	logger := log.FromContext(ctx)
	logger.Info("reconciling shared registry config daemonset")
	desiredDaemonSet := r.registryBuilder.BuildRegistryConfigReconcilerDaemonset(ctx, registry, nodeRegistryAccessConfigMapName, "registries.json")
	existingDaemonSet := &appsv1.DaemonSet{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(desiredDaemonSet), existingDaemonSet); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Client.Create(ctx, desiredDaemonSet)
		}
		return resultNil, err
	}

	if existingDaemonSet.Spec.Template.Spec.Containers[0].Image != desiredDaemonSet.Spec.Template.Spec.Containers[0].Image {
		updatedObj := existingDaemonSet.DeepCopy()
		updatedObj.Spec.Template.Spec.Containers[0].Image = desiredDaemonSet.Spec.Template.Spec.Containers[0].Image
		return resultRequeue, r.Client.Update(ctx, updatedObj)
	}

	if existingDaemonSet.Status.DesiredNumberScheduled == existingDaemonSet.Status.NumberAvailable &&
		existingDaemonSet.Status.DesiredNumberScheduled == existingDaemonSet.Status.NumberReady {
		meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{
			Type:               string(registryv1alpha1.RegistryReady),
			Status:             metav1.ConditionTrue,
			Reason:             "RegistryDeploymentAndServiceAvailable",
			Message:            "Registry Deployment and Service are Available",
			ObservedGeneration: registry.Generation,
		})
		registry.Status.Phase = registryv1alpha1.RegistryPhaseRunning
		registry.Status.ObservedGeneration = registry.Generation
		return resultNil, nil
	}
	meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{
		Type:               string(registryv1alpha1.RegistryReady),
		Status:             metav1.ConditionFalse,
		Reason:             "RegistryConfigReconcilerDaemonsetNotAvailable",
		Message:            "RegistryConfigReconcilerDaemonset is not Available",
		ObservedGeneration: registry.Generation,
	})
	registry.Status.Phase = registryv1alpha1.RegistryPhaseFailed
	registry.Status.ObservedGeneration = registry.Generation
	return resultRequeue, nil
}

// TODO: Handle removal of hosts from CM and finally delete the daemonset if required.
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
	for _, sr := range r.subReconcilers {
		logger := log.FromContext(ctx)
		logger.Info("reconciling", "sub-reconciler", sr.name)
		result, err := sr.reconcile(ctx, registry)
		switch {
		case err != nil:
			logger.Error(err, "failed to reconcile", "sub-reconciler", sr.name)
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
	registry.Status.InternalURL = registryURL
	registry.Status.ServiceIP = existingService.Spec.ClusterIP
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
	if registry.Spec.Auth == nil {
		return resultNil, nil
	}
	if registry.Spec.Auth.HtPasswordCredentials != nil {
		return r.reconcileHtPasswordAuthSecret(ctx, registry)
	}
	return resultNil, nil
}

func (r *RegistryReconciler) reconcileHtPasswordAuthSecret(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (subReconcilerResult, error) {
	credentialsInfo := registry.Spec.Auth.HtPasswordCredentials.CredentialsRef
	htpasswdSecret := &corev1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      credentialsInfo.SecretRef.Name,
		Namespace: credentialsInfo.SecretRef.Namespace,
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

	password := string(htpasswdSecret.Data[credentialsInfo.PasswordKey])
	if len(password) == 0 {
		// set status condition
		meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{
			Type:               string(registryv1alpha1.RegistryReady),
			Status:             metav1.ConditionFalse,
			Reason:             "HtPasswordAuthSecretError",
			Message:            "Missing password in secret",
			ObservedGeneration: registry.Generation,
		})
		registry.Status.InternalURL = ""
		registry.Status.Phase = registryv1alpha1.RegistryPhasePending
		registry.Status.ObservedGeneration = registry.Generation
		return resultStop, nil
	}

	username := string(htpasswdSecret.Data[credentialsInfo.UsernameKey])
	if len(username) == 0 {
		// set status condition
		meta.SetStatusCondition(&registry.Status.Conditions, metav1.Condition{
			Type:               string(registryv1alpha1.RegistryReady),
			Status:             metav1.ConditionFalse,
			Reason:             "HtPasswordAuthSecretError",
			Message:            "Missing username in secret",
			ObservedGeneration: registry.Generation,
		})
		registry.Status.InternalURL = ""
		registry.Status.Phase = registryv1alpha1.RegistryPhasePending
		registry.Status.ObservedGeneration = registry.Generation
		return resultStop, nil
	}
	desiredSecret, secretKey, err := r.registryBuilder.BuildHTPasswordSecret(ctx, registry, username, password)
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
