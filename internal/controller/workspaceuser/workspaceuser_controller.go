package workspaceuser

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	stackv1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	userv1alpha1 "stackdome.io/cluster-agent/api/users/v1alpha1"

	"stackdome.io/cluster-agent/internal/controller"
)

const (
	WorkspaceUserLabel = "workspaceuser.stackdome.io/workspace-user"
)

type subReconcilerResult struct {
	resultStop         bool
	resultRequeue      bool
	resultRequeueAfter *time.Duration
}

var (
	resultNil          = subReconcilerResult{}
	resultStop         = subReconcilerResult{resultStop: true}
	resultRequeue      = subReconcilerResult{resultRequeue: true}
	resultRequeueAfter = func(t time.Duration) subReconcilerResult {
		return subReconcilerResult{resultRequeueAfter: &t}
	}
)

func (s *subReconcilerResult) IsZero() bool {
	return s == nil || *s == resultNil
}

// WorkspaceUserReconciler reconciles a WorkspaceUser object
type WorkspaceUserReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *WorkspaceUserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.WithValues("workspaceuser", req.NamespacedName.String())
	logger.Info("In workspace user reconciler")
	ctx = controller.ContextWithLogger(ctx, logger)
	workspaceUser := &userv1alpha1.User{}
	if err := r.Client.Get(ctx, req.NamespacedName, workspaceUser); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	res, err := r.reconcile(ctx, workspaceUser)
	if err != nil {
		return res, err
	}
	return res, r.Status().Update(ctx, workspaceUser)
}

func (r *WorkspaceUserReconciler) reconcile(ctx context.Context, user *userv1alpha1.User) (ctrl.Result, error) {
	reconcileFns := []func(ctx context.Context, user *userv1alpha1.User) (subReconcilerResult, error){
		r.reconcileConfigNamespace,
		r.reconcileNamespaces,
		r.reconcileSA,
		r.reconcileRole,
		r.reconcileRoleBinding,
		r.reconcileSASecret,
		r.statusReconciler,
	}
	for _, reconcileFn := range reconcileFns {
		res, err := reconcileFn(ctx, user)
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
	return ctrl.Result{}, nil
}

func (r *WorkspaceUserReconciler) reconcileConfigNamespace(ctx context.Context, user *userv1alpha1.User) (subReconcilerResult, error) {
	logger := log.FromContext(ctx)
	logger.Info("in reconcileConfigNamespace")
	configNamespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: UserConfigNamespace(user),
		},
	}
	if err := controllerutil.SetControllerReference(user, configNamespace, r.Scheme); err != nil {
		return resultNil, err
	}
	existingConfigNamespace := &corev1.Namespace{}
	if err := r.Get(ctx, controller.GetNamespacedName(configNamespace), existingConfigNamespace); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Create(ctx, configNamespace)
		}
		return resultRequeue, err
	}
	ownedByUs := controller.HasSameController(existingConfigNamespace, configNamespace)
	if !ownedByUs {
		existingConfigNamespace.OwnerReferences = configNamespace.OwnerReferences
		return resultRequeue, r.Update(ctx, existingConfigNamespace)
	}
	return resultNil, nil
}

func (r *WorkspaceUserReconciler) statusReconciler(ctx context.Context, user *userv1alpha1.User) (subReconcilerResult, error) {
	objectStackdomeServerVersion, ok := user.Labels[stackv1alpha1.StackdomeObjectGeneration]
	if ok {
		generation, _ := strconv.ParseInt(objectStackdomeServerVersion, 10, 64)
		user.Status.ObservedStackdomeServerObjectGeneration = generation
	}
	existingSecret := &corev1.Secret{}
	if err := r.Get(
		ctx,
		types.NamespacedName{
			Namespace: UserConfigNamespace(user),
			Name:      ServiceAccountSecretName(user),
		}, existingSecret); err != nil {
		return resultNil, err
	}
	SAtoken, present := existingSecret.Data[corev1.ServiceAccountTokenKey]
	if !present {
		meta.SetStatusCondition(&user.Status.Conditions, metav1.Condition{
			Type:               userv1alpha1.UserAvailable,
			ObservedGeneration: user.Generation,
			Status:             metav1.ConditionFalse,
			Reason:             "WorkspaceUserNotYetReady",
			Message:            "WorkspaceUser not yet ready. Missing service account token",
		})
		return resultRequeue, nil
	}

	userCopy := user.DeepCopy()
	user.Status.Namespaces = userCopy.Spec.Namespaces
	user.Status.Phase = userv1alpha1.UserPhasePhaseReady
	user.Status.ServiceAccountName = ServiceAccountName(user)
	user.Status.ServiceAccountToken = string(SAtoken)
	meta.SetStatusCondition(&user.Status.Conditions, metav1.Condition{
		Type:               userv1alpha1.UserAvailable,
		ObservedGeneration: user.Generation,
		Status:             metav1.ConditionTrue,
		Reason:             "WorkspaceUserReady",
		Message:            "WorkspaceUser ready",
	})
	user.Status.StatusHash = user.StatusHash()
	return resultNil, nil
}

func (r *WorkspaceUserReconciler) reconcileSA(ctx context.Context, user *userv1alpha1.User) (subReconcilerResult, error) {
	logger := log.FromContext(ctx)
	logger.Info("in reconcileSA")
	desiredServiceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceAccountName(user),
			Namespace: UserConfigNamespace(user),
		},
	}
	if err := controllerutil.SetControllerReference(user, desiredServiceAccount, r.Scheme); err != nil {
		return resultNil, err
	}

	existingServiceAccount := &corev1.ServiceAccount{}
	err := r.Client.Get(ctx, controller.GetNamespacedName(desiredServiceAccount), existingServiceAccount)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Create(ctx, desiredServiceAccount)
		} else {
			return resultNil, fmt.Errorf("failed to get ServiceAccount: %v", err)
		}
	}

	ownedByUs := controller.HasSameController(existingServiceAccount, desiredServiceAccount)
	if !ownedByUs {
		existingServiceAccount.OwnerReferences = desiredServiceAccount.OwnerReferences
		return resultRequeue, r.Update(ctx, existingServiceAccount)
	}
	return resultNil, nil

}

func (r *WorkspaceUserReconciler) reconcileRole(ctx context.Context, user *userv1alpha1.User) (subReconcilerResult, error) {
	desiredClusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: UserRoleName(user),
		},
		Rules: user.Spec.AccessRules,
	}
	if err := controllerutil.SetControllerReference(user, desiredClusterRole, r.Scheme); err != nil {
		return resultNil, err
	}
	existingClusterRole := &rbacv1.ClusterRole{}
	if err := r.Get(ctx, controller.GetNamespacedName(desiredClusterRole), existingClusterRole); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Client.Create(ctx, desiredClusterRole)
		}
		return resultNil, err
	}

	ownedByUs := controller.HasSameController(existingClusterRole, desiredClusterRole)
	specChanged := !equality.Semantic.DeepDerivative(desiredClusterRole.Rules, existingClusterRole.Rules)
	if !ownedByUs || specChanged {
		existingClusterRole.OwnerReferences = desiredClusterRole.OwnerReferences
		existingClusterRole.Rules = desiredClusterRole.Rules
		return resultRequeue, r.Update(ctx, existingClusterRole)
	}
	return resultNil, nil
}

// TODO: Remove rolebindings when namespaces are removed.
func (r *WorkspaceUserReconciler) reconcileRoleBinding(ctx context.Context, user *userv1alpha1.User) (subReconcilerResult, error) {
	desiredRoleBindings := make([]*rbacv1.RoleBinding, 0)
	for _, ns := range user.Spec.Namespaces {
		desiredRoleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      UserRoleBindingname(user),
				Namespace: ns,
				Labels: map[string]string{
					WorkspaceUserLabel: user.Name,
				},
			},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      ServiceAccountName(user),
					Namespace: UserConfigNamespace(user),
				},
			},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				Name:     UserRoleName(user),
				APIGroup: rbacv1.GroupName,
			},
		}
		if err := controllerutil.SetControllerReference(user, desiredRoleBinding, r.Scheme); err != nil {
			return resultNil, err
		}
		desiredRoleBindings = append(desiredRoleBindings, desiredRoleBinding)
	}
	for _, desiredRoleBinding := range desiredRoleBindings {
		existingRoleBinding := &rbacv1.RoleBinding{}
		if err := r.Get(ctx, controller.GetNamespacedName(desiredRoleBinding), existingRoleBinding); err != nil {
			if apierrors.IsNotFound(err) {
				return resultRequeue, r.Client.Create(ctx, desiredRoleBinding)
			}
			return resultNil, fmt.Errorf("failed to get RoleBinding: %v", err)
		}
		ownedByUs := controller.HasSameController(existingRoleBinding, desiredRoleBinding)
		if !ownedByUs {
			existingRoleBinding.OwnerReferences = desiredRoleBinding.OwnerReferences
			return resultRequeue, r.Update(ctx, existingRoleBinding)
		}
	}

	// Remove unwanted rolebindings.
	roleBindingList := &rbacv1.RoleBindingList{}
	if err := r.Client.List(ctx, roleBindingList, client.MatchingLabels{WorkspaceUserLabel: user.Name}); err != nil {
		return resultNil, err
	}
	for _, roleBinding := range roleBindingList.Items {
		if !slices.Contains(user.Spec.Namespaces, roleBinding.Namespace) {
			if err := r.Client.Delete(ctx, &roleBinding, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
				return resultNil, err
			}
		}
	}
	return resultNil, nil
}

func (r *WorkspaceUserReconciler) reconcileSASecret(ctx context.Context, user *userv1alpha1.User) (subReconcilerResult, error) {
	desiredSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceAccountSecretName(user),
			Namespace: UserConfigNamespace(user),
			Annotations: map[string]string{
				corev1.ServiceAccountNameKey: ServiceAccountName(user),
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	}

	if err := controllerutil.SetControllerReference(user, desiredSecret, r.Scheme); err != nil {
		return resultNil, err
	}

	existingSecret := &corev1.Secret{}
	if err := r.Get(ctx, controller.GetNamespacedName(desiredSecret), existingSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Client.Create(ctx, desiredSecret)
		}
		return resultNil, err
	}
	ownedByUs := controller.HasSameController(desiredSecret, existingSecret)
	if !ownedByUs {
		existingSecret.OwnerReferences = desiredSecret.OwnerReferences
		return resultRequeue, r.Update(ctx, existingSecret)
	}
	return resultNil, nil
}

func (r *WorkspaceUserReconciler) reconcileNamespaces(ctx context.Context, config *userv1alpha1.User) (subReconcilerResult, error) {
	desiredNamespaces := make([]corev1.Namespace, 0)
	for _, ns := range config.Spec.Namespaces {
		desiredNS := corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: ns,
				Labels: map[string]string{
					WorkspaceUserLabel: config.Name,
				},
			},
			Spec: corev1.NamespaceSpec{},
		}

		if err := controllerutil.SetControllerReference(config, &desiredNS, r.Scheme); err != nil {
			return resultNil, err
		}
		desiredNamespaces = append(desiredNamespaces, desiredNS)
	}

	for _, desiredNS := range desiredNamespaces {
		existingNS := &corev1.Namespace{}
		if err := r.Client.Get(ctx, controller.GetNamespacedName(&desiredNS), existingNS); err != nil {
			if apierrors.IsNotFound(err) {
				return resultRequeue, r.Client.Create(ctx, &desiredNS)
			}
			return resultNil, err
		}
		ownedByUs := controller.HasSameController(existingNS, &desiredNS)

		if !ownedByUs {
			existingNS.OwnerReferences = desiredNS.OwnerReferences
			return resultRequeue, r.Client.Update(ctx, existingNS)
		}
	}

	nsList := &corev1.NamespaceList{}
	if err := r.Client.List(ctx, nsList, client.MatchingLabels{WorkspaceUserLabel: config.Name}); err != nil {
		return resultNil, err
	}
	// Remove unwanted namespaces.
	for _, ns := range nsList.Items {
		if !slices.Contains(config.Spec.Namespaces, ns.Name) {
			if err := r.Client.Delete(ctx, &ns, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil {
				return resultNil, err
			}
		}
	}

	return resultNil, nil
}

// TODO: Move this to the volume sync controller.
func (r *WorkspaceUserReconciler) reconcileBusyBoxBinaryVolume(ctx context.Context, config *userv1alpha1.User) (subReconcilerResult, error) {
	jobName := "copy-busybox-binary-to-volume"
	namespace := UserConfigNamespace(config)
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
		return resultNil, err
	}

	existingPVC := &corev1.PersistentVolumeClaim{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: namespace}, existingPVC); err != nil {
		if apierrors.IsNotFound(err) {
			// Allow some time to provision the PVC.
			return resultRequeueAfter(time.Second * 5), r.Client.Create(ctx, pvc)
		}
		return resultNil, err
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
							Image: "docker.io/ashishmax31327/stackdome-tools:1",
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
		return resultNil, err
	}

	// Define the Job
	existingJob := &batchv1.Job{}
	if err := r.Get(ctx, client.ObjectKey{Name: jobName, Namespace: namespace}, existingJob); err != nil {
		if apierrors.IsNotFound(err) {
			meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
				Type:               userv1alpha1.UserAvailable,
				ObservedGeneration: config.Generation,
				Status:             metav1.ConditionFalse,
				Reason:             "BusyBoxBinaryNotYetCopied",
				Message:            "WorkspaceConfiguration not yet ready. BusyBox binary not yet copied.",
			})
			return resultRequeue, r.Create(ctx, desiredJob)
		}
		return resultNil, err
	}

	JobCompletedCondition := findJobCompleteCondition(existingJob)

	if JobCompletedCondition != nil && JobCompletedCondition.Status == corev1.ConditionStatus(metav1.ConditionTrue) {
		return resultNil, nil
	}

	logger := log.FromContext(ctx)
	logger.Info("Job not yet completed")
	meta.SetStatusCondition(&config.Status.Conditions, metav1.Condition{
		Type:               userv1alpha1.UserAvailable,
		ObservedGeneration: config.Generation,
		Status:             metav1.ConditionFalse,
		Reason:             "BusyBoxBinaryNotYetCopied",
		Message:            "WorkspaceConfiguration not yet ready. BusyBox binary not yet copied.",
	})
	return resultStop, nil
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
func (r *WorkspaceUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&userv1alpha1.User{}).
		Owns(&corev1.Namespace{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&corev1.Secret{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
