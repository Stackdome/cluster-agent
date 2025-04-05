package storage

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8sstoragev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
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

const (
	nfsStorageController = "nfs-storage-controller"

	cacheFinalizer = "nfsserver.stackdome.io/cache"
)

type StorageClassOpts struct {
	Name                 string
	ProvisionerName      string
	ReclaimPolicy        *corev1.PersistentVolumeReclaimPolicy
	MountOptions         []string
	AllowVolumeExpansion *bool
}

// NFSServerReconciler reconciles a NFSServer object
type NFSServerReconciler struct {
	StorageClassOpts *StorageClassOpts
	NfsServerImage   string
	Client           client.Client
	Scheme           *runtime.Scheme
	subReconcilers   []func(context.Context, *storagev1alpha1.NFSServer) (subReconcilerResult, error)
}

func NewNFSServerReconciler(uncachedClient client.Client, cachedClient client.Client, scheme *runtime.Scheme, storageClassOpts StorageClassOpts, nfsServerImage string) (*NFSServerReconciler, error) {
	desiredStorageClass := &k8sstoragev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: storageClassOpts.Name,
		},
		Provisioner:          storageClassOpts.ProvisionerName,
		ReclaimPolicy:        storageClassOpts.ReclaimPolicy,
		MountOptions:         storageClassOpts.MountOptions,
		AllowVolumeExpansion: storageClassOpts.AllowVolumeExpansion,
		VolumeBindingMode:    ptr.To(k8sstoragev1.VolumeBindingWaitForFirstConsumer),
	}

	if scCreateErr := uncachedClient.Create(context.Background(), desiredStorageClass); scCreateErr != nil {
		if !apierrors.IsAlreadyExists(scCreateErr) {
			return nil, scCreateErr
		}
	}

	if len(nfsServerImage) == 0 {
		return nil, fmt.Errorf("nfs server image is required")
	}

	r := &NFSServerReconciler{
		NfsServerImage: nfsServerImage,
		Client:         cachedClient,
		Scheme:         scheme,
	}

	r.subReconcilers = []func(context.Context, *storagev1alpha1.NFSServer) (subReconcilerResult, error){
		r.reconcileBackendStorage,
		r.reconcileNFSServerDeployment,
		r.reconcileNFSServerService,
	}
	return r, nil
}

func (r *NFSServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling NFSServer", "namespace", req.Namespace, "name", req.Name)

	nfsServer := &storagev1alpha1.NFSServer{}
	if err := r.Client.Get(ctx, req.NamespacedName, nfsServer); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if nfsServer.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(nfsServer, cacheFinalizer) {
			deleteRes, err := r.reconcileDelete(ctx, nfsServer)
			if err != nil {
				return ctrl.Result{}, err
			}
			if !deleteRes.IsZero() {
				return deleteRes, nil
			}
			controllerutil.RemoveFinalizer(nfsServer, cacheFinalizer)
			return ctrl.Result{}, r.Client.Update(ctx, nfsServer)
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(nfsServer, cacheFinalizer) {
		controllerutil.AddFinalizer(nfsServer, cacheFinalizer)
		return ctrl.Result{}, r.Client.Update(ctx, nfsServer)
	}

	for _, subReconciler := range r.subReconcilers {
		res, err := subReconciler(ctx, nfsServer)
		switch {
		case err != nil:
			return ctrl.Result{}, err
		case res.resultStop:
			return ctrl.Result{}, nil
		case res.resultRequeue:
			return ctrl.Result{Requeue: true}, nil
		case res.resultRequeueAfter != nil:
			return ctrl.Result{RequeueAfter: *res.resultRequeueAfter}, nil
		}
	}

	meta.SetStatusCondition(&nfsServer.Status.Conditions, metav1.Condition{
		Type:    string(storagev1alpha1.NFSServerConditionTypeAvailable),
		Status:  metav1.ConditionTrue,
		Reason:  "NFSServerAvailable",
		Message: "NFSServer deployment is available",
	})

	nfsServer.Status.Phase = storagev1alpha1.NFSServerPhaseReady

	return ctrl.Result{}, r.Client.Status().Update(ctx, nfsServer)
}

// TODO: Implement correct deletion behaviour.
func (r *NFSServerReconciler) reconcileDelete(ctx context.Context, nfsServer *storagev1alpha1.NFSServer) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (r *NFSServerReconciler) reconcileBackendStorage(ctx context.Context, nfsServer *storagev1alpha1.NFSServer) (subReconcilerResult, error) {
	storageSize, err := k8sresource.ParseQuantity(nfsServer.Spec.Capacity)
	if err != nil {
		return resultNil, err
	}

	desiredBackendStoragePVC := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nfsServer.BackendPVCName(),
			Namespace: nfsServer.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: nfsServer.Spec.BackingStorageClassName,
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageSize,
				},
			},
		},
	}

	if err := controllerutil.SetOwnerReference(nfsServer, desiredBackendStoragePVC, r.Scheme); err != nil {
		return resultNil, err
	}

	existingBackendStoragePVC := &corev1.PersistentVolumeClaim{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(desiredBackendStoragePVC), existingBackendStoragePVC); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Client.Create(ctx, desiredBackendStoragePVC)
		}
		return resultNil, err
	}

	return resultNil, nil
}

func (r *NFSServerReconciler) reconcileNFSServerDeployment(ctx context.Context, nfsServer *storagev1alpha1.NFSServer) (subReconcilerResult, error) {
	nfsServerDeploymentName := fmt.Sprintf("%s-nfs-server", nfsServer.Name)
	desiredDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nfsServerDeploymentName,
			Namespace: nfsServer.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": nfsServerDeploymentName},
			},
			Replicas: ptr.To(int32(1)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": nfsServerDeploymentName},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "nfs-server",
							Image: r.NfsServerImage,
							Ports: []corev1.ContainerPort{
								{ContainerPort: 2049},
								{ContainerPort: 20048},
								{ContainerPort: 111},
							},
							SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(true)},
							Env: []corev1.EnvVar{
								{Name: "NFS_EXPORT_0", Value: nfsServer.Spec.ExportDir},
								{Name: "NFS_DISABLE_VERSION_3", Value: "1"},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "export", MountPath: nfsServer.Spec.ExportDir},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "export",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: nfsServer.BackendPVCName(),
								},
							},
						},
					},
				},
			},
		},
	}

	desiredDeployment.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment"))

	if err := controllerutil.SetControllerReference(nfsServer, desiredDeployment, r.Scheme); err != nil {
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
		FieldManager: nfsStorageController,
	}); err != nil {
		return resultNil, err
	}

	if controller.DeploymentAvailable(desiredDeployment) {
		return resultNil, nil
	}

	meta.SetStatusCondition(&nfsServer.Status.Conditions, metav1.Condition{
		Type:    string(storagev1alpha1.NFSServerConditionTypeAvailable),
		Status:  metav1.ConditionFalse,
		Reason:  "NFSServerDeploymentNotReady",
		Message: "NFSServer deployment is not yet available",
	})
	nfsServer.Status.Phase = storagev1alpha1.NFSServerPhasePending
	return resultStop, nil
}

func (r *NFSServerReconciler) reconcileNFSServerService(ctx context.Context, nfsServer *storagev1alpha1.NFSServer) (subReconcilerResult, error) {
	nfsServerDeploymentName := fmt.Sprintf("%s-nfs-server", nfsServer.Name)
	desiredService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nfsServer.Name,
			Namespace: nfsServer.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": nfsServerDeploymentName},
			Ports: []corev1.ServicePort{
				{
					Name: "nfs",
					Port: 2049,
				},
				{
					Name: "mountd",
					Port: 20048,
				},
				{
					Name: "rpcbind",
					Port: 111,
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(nfsServer, desiredService, r.Scheme); err != nil {
		return resultNil, err
	}

	existingService := &corev1.Service{}
	if err := r.Client.Get(ctx, client.ObjectKeyFromObject(desiredService), existingService); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Client.Create(ctx, desiredService)
		}
		return resultNil, err
	}

	svcURL := existingService.Spec.ClusterIP
	nfsServer.Status.NFSServerURL = svcURL
	return resultNil, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NFSServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&storagev1alpha1.NFSServer{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Named("storage-nfsserver").
		Complete(r)
}
