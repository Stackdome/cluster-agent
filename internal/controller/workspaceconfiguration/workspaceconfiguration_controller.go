package workspaceconfiguration

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceConfigurationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&workspacev1alpha1.WorkspaceConfiguration{}).
		Owns(&corev1.Namespace{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}
