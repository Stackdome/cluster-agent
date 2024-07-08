package workspaceconfiguration

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	workspacev1alpha1 "soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

// TODO: DONT do this!. Make this granular.
var workspaceUserAccessRules = []rbacv1.PolicyRule{
	{
		APIGroups: []string{"*"},
		Resources: []string{"*"},
		Verbs:     []string{"*"},
	},
}

// WorkspaceConfigurationReconciler reconciles a WorkspaceConfiguration object
type WorkspaceConfigurationReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *WorkspaceConfigurationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.WithValues("workspaceconfiguration", req.NamespacedName.String())
	logger.Info("IN workspace configuration reconciler")
	ctx = controller.ContextWithLogger(ctx, logger)

	workspaceConfig := &workspacev1alpha1.WorkspaceConfiguration{}
	if err := r.Client.Get(ctx, req.NamespacedName, workspaceConfig); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	res, err := r.reconcile(ctx, workspaceConfig)
	if err != nil {
		return res, err
	}
	return res, r.Status().Update(ctx, workspaceConfig)
}

func (r *WorkspaceConfigurationReconciler) reconcile(ctx context.Context, config *workspacev1alpha1.WorkspaceConfiguration) (ctrl.Result, error) {
	reconcileFns := []func(ctx context.Context, config *workspacev1alpha1.WorkspaceConfiguration) (ctrl.Result, error){
		r.reconcileNamespace,
		r.reconcileSA,
		r.reconcileRole,
		r.reconcileRoleBinding,
		r.reconcileSASecret,
		r.reconcileBusyBoxBinaryVolume,
		r.statusReconciler,
	}
	for _, reconcileFn := range reconcileFns {
		res, err := reconcileFn(ctx, config)
		switch {
		case err != nil:
			return ctrl.Result{}, err
		case !res.IsZero():
			return res, err
		default:
		}
	}
	return ctrl.Result{}, nil
}

func (r *WorkspaceConfigurationReconciler) statusReconciler(ctx context.Context, config *workspacev1alpha1.WorkspaceConfiguration) (ctrl.Result, error) {
	config.Status.ObservedGeneration = config.Generation
	existingSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: config.Spec.WorkspaceNamespace, Name: ServiceAccountSecretName(config)}, existingSecret); err != nil {
		return ctrl.Result{}, err
	}
	SAtoken, present := existingSecret.Data[corev1.ServiceAccountTokenKey]
	if !present {
		meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
			Type:               workspacev1alpha1.WorkspaceConfigurationAvailable,
			ObservedGeneration: config.Generation,
			Status:             metav1.ConditionFalse,
			Reason:             "WorkspaceConfigurationNotYetReady",
			Message:            "WorkspaceConfiguration not yet ready. Missing service account token",
		})
		return ctrl.Result{Requeue: true}, nil
	}

	configCopy := config.DeepCopy()
	config.Status.Namespace = ptr.To(configCopy.Spec.WorkspaceNamespace)
	config.Status.Phase = workspacev1alpha1.WorkspaceConfigurationPhaseReady
	config.Status.ServiceAccountName = ptr.To(ServiceAccountName(config))
	config.Status.ServiceAccountToken = ptr.To(string(SAtoken))
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               workspacev1alpha1.WorkspaceConfigurationAvailable,
		ObservedGeneration: config.Generation,
		Status:             metav1.ConditionTrue,
		Reason:             "WorkspaceConfigurationReady",
		Message:            "WorkspaceConfiguration ready",
	})
	return ctrl.Result{}, nil
}

func (r *WorkspaceConfigurationReconciler) reconcileSA(ctx context.Context, config *workspacev1alpha1.WorkspaceConfiguration) (ctrl.Result, error) {
	desiredServiceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceAccountName(config),
			Namespace: config.Spec.WorkspaceNamespace,
		},
	}
	if err := controllerutil.SetControllerReference(config, desiredServiceAccount, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	existingServiceAccount := &corev1.ServiceAccount{}
	err := r.Client.Get(ctx, controller.GetNamespacedName(desiredServiceAccount), existingServiceAccount)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.Create(ctx, desiredServiceAccount)
		} else {
			return ctrl.Result{}, fmt.Errorf("failed to get ServiceAccount: %v", err)
		}
	}

	ownedByUs := controller.HasSameController(existingServiceAccount, desiredServiceAccount)
	if !ownedByUs {
		existingServiceAccount.OwnerReferences = desiredServiceAccount.OwnerReferences
		return ctrl.Result{}, r.Update(ctx, existingServiceAccount)
	}
	return ctrl.Result{}, nil

}

func (r *WorkspaceConfigurationReconciler) reconcileRole(ctx context.Context, config *workspacev1alpha1.WorkspaceConfiguration) (ctrl.Result, error) {
	desiredRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      UserRoleName(config),
			Namespace: config.Spec.WorkspaceNamespace,
		},
		Rules: workspaceUserAccessRules,
	}
	if err := controllerutil.SetControllerReference(config, desiredRole, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	existingRole := &rbacv1.Role{}
	if err := r.Get(ctx, controller.GetNamespacedName(desiredRole), existingRole); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.Client.Create(ctx, desiredRole)
		}
		return ctrl.Result{}, err
	}

	ownedByUs := controller.HasSameController(existingRole, desiredRole)
	if !ownedByUs {
		existingRole.OwnerReferences = desiredRole.OwnerReferences
		return ctrl.Result{}, r.Update(ctx, existingRole)
	}
	return ctrl.Result{}, nil

}

func (r *WorkspaceConfigurationReconciler) reconcileRoleBinding(ctx context.Context, config *workspacev1alpha1.WorkspaceConfiguration) (ctrl.Result, error) {
	desiredRoleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      UserRoleBindingname(config),
			Namespace: config.Spec.WorkspaceNamespace,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      ServiceAccountName(config),
				Namespace: config.Spec.WorkspaceNamespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     UserRoleName(config),
			APIGroup: rbacv1.GroupName,
		},
	}
	if err := controllerutil.SetControllerReference(config, desiredRoleBinding, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	// Fetch the RoleBinding instance
	existingRoleBinding := &rbacv1.RoleBinding{}
	if err := r.Get(ctx, controller.GetNamespacedName(desiredRoleBinding), existingRoleBinding); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.Client.Create(ctx, desiredRoleBinding)
		}
		return ctrl.Result{}, fmt.Errorf("failed to get RoleBinding: %v", err)
	}
	ownedByUs := controller.HasSameController(existingRoleBinding, desiredRoleBinding)
	if !ownedByUs {
		existingRoleBinding.OwnerReferences = desiredRoleBinding.OwnerReferences
		return ctrl.Result{}, r.Update(ctx, existingRoleBinding)
	}
	return ctrl.Result{}, nil

}

func (r *WorkspaceConfigurationReconciler) reconcileSASecret(ctx context.Context, config *workspacev1alpha1.WorkspaceConfiguration) (ctrl.Result, error) {
	desiredSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceAccountSecretName(config),
			Namespace: config.Spec.WorkspaceNamespace,
			Annotations: map[string]string{
				corev1.ServiceAccountNameKey: ServiceAccountName(config),
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}

	if err := controllerutil.SetControllerReference(config, desiredSecret, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	existingSecret := &corev1.Secret{}
	if err := r.Get(ctx, controller.GetNamespacedName(desiredSecret), existingSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.Client.Create(ctx, desiredSecret)
		}
		return ctrl.Result{}, err
	}
	ownedByUs := controller.HasSameController(desiredSecret, existingSecret)
	if !ownedByUs {
		existingSecret.OwnerReferences = desiredSecret.OwnerReferences
		return ctrl.Result{}, r.Update(ctx, existingSecret)
	}
	return ctrl.Result{}, nil
}

func (r *WorkspaceConfigurationReconciler) reconcileNamespace(ctx context.Context, config *workspacev1alpha1.WorkspaceConfiguration) (ctrl.Result, error) {
	desiredNS := corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: config.Spec.WorkspaceNamespace,
		},
		// TODO: Add NS finalizers.
		Spec: corev1.NamespaceSpec{},
	}

	if err := controllerutil.SetControllerReference(config, &desiredNS, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	existingNS := &corev1.Namespace{}
	if err := r.Client.Get(ctx, controller.GetNamespacedName(&desiredNS), existingNS); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{Requeue: true}, r.Client.Create(ctx, &desiredNS)
		}
		return ctrl.Result{}, err
	}

	ownedByUs := controller.HasSameController(existingNS, &desiredNS)
	if !ownedByUs {
		existingNS.OwnerReferences = desiredNS.OwnerReferences
		return ctrl.Result{Requeue: true}, r.Client.Update(ctx, existingNS)
	}
	// NOOP.
	return ctrl.Result{}, nil
}
func (r *WorkspaceConfigurationReconciler) reconcileBusyBoxBinaryVolume(ctx context.Context, config *workspacev1alpha1.WorkspaceConfiguration) (ctrl.Result, error) {
	jobName := "copy-busybox-binary-to-volume"
	namespace := config.Spec.WorkspaceNamespace
	pvcName := BusyBoxPVCName()

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("500Mi")},
			},
		},
	}

	if err := controllerutil.SetControllerReference(config, pvc, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	existingPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, existingPVC); err != nil {
		if apierrors.IsNotFound(err) {
			// Allow some time to provision the PVC.
			return ctrl.Result{RequeueAfter: time.Second * 5}, r.Client.Create(ctx, pvc)
		}
		return ctrl.Result{}, err
	}

	desiredJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "rsync",
							Image: "asia-south1-docker.pkg.dev/stackdome/stackdome/rsync-busybox:1",
							Command: []string{
								"sh",
								"-c",
							},
							Args: []string{"cp /app/rsync /binaries/rsync && chmod +x /binaries/rsync && cp /bin/busybox /binaries/busybox && chmod +x /binaries/busybox"},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "binaries",
									MountPath: "/binaries",
								},
							},
						},
					},
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Volumes: []corev1.Volume{
						{
							Name: "binaries",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvcName,
								},
							},
						},
					},
				},
			},
		},
	}

	// Set the WorkspaceConfiguration instance as the owner and controller
	if err := controllerutil.SetControllerReference(config, desiredJob, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	// Define the Job
	existingJob := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Name: jobName, Namespace: namespace}, existingJob); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.Create(ctx, desiredJob)
		}
		return ctrl.Result{}, err
	}

	JobCompletedCondition := findJobCompleteCondition(existingJob)

	if JobCompletedCondition != nil && JobCompletedCondition.Status == corev1.ConditionStatus(metav1.ConditionTrue) {
		return ctrl.Result{}, nil
	}

	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               workspacev1alpha1.WorkspaceConfigurationAvailable,
		ObservedGeneration: config.Generation,
		Status:             metav1.ConditionFalse,
		Reason:             "BusyBoxBinaryNotYetCopied",
		Message:            "WorkspaceConfiguration not yet ready. BusyBox binary not yet copied.",
	})
	return ctrl.Result{}, nil
}

func findJobCompleteCondition(job *batchv1.Job) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		if job.Status.Conditions[i].Type == batchv1.JobComplete {
			return &job.Status.Conditions[i]
		}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&workspacev1alpha1.WorkspaceConfiguration{}).
		Owns(&corev1.Namespace{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&corev1.Secret{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
