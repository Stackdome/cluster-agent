package applicationbuild

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
	"soradev.io/cluster-agent/pkg/imagebuilder"
)

// WorkspaceApplicationBuildReconciler reconciles a WorkspaceApplicationBuild object
type WorkspaceApplicationBuildReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *WorkspaceApplicationBuildReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ctx = controller.ContextWithLogger(ctx, logger.WithValues("WorkspaceApplicationBuild", req.String()))
	applicationBuild := &v1alpha1.WorkspaceApplicationBuild{}

	if err := r.Client.Get(ctx, req.NamespacedName, applicationBuild); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	res, err := r.reconcile(ctx, applicationBuild)
	if err != nil {
		return res, err
	}
	return res, r.Client.Status().Update(ctx, applicationBuild)
}

func reportWorkspaceApplicationBuildComplete(buildConfig *v1alpha1.WorkspaceApplicationBuild) {
	buildConfig.Status.Phase = v1alpha1.WorkspaceApplicationBuildPhaseSuccess
	buildConfig.Status.BuildSourceHash = buildConfig.Spec.SourceHash
	meta.SetStatusCondition(&buildConfig.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceApplicationBuildAvailable),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: buildConfig.Generation,
		Reason:             "BuildComplete",
		Message:            "Image build compelete",
	})
}

func (r *WorkspaceApplicationBuildReconciler) reconcile(ctx context.Context, buildConfig *v1alpha1.WorkspaceApplicationBuild) (ctrl.Result, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconciling application build")
	volumeRef := &v1alpha1.WorkspaceVolume{}

	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      buildConfig.Spec.ContextRef.VolumeName,
		Namespace: buildConfig.Namespace,
	}, volumeRef); err != nil {
		return ctrl.Result{}, err
	}

	if !volumeAvailable(volumeRef) {
		reportWorkspaceApplicationBuildStatus(
			buildConfig,
			v1alpha1.WorkspaceApplicationBuildAvailable,
			metav1.ConditionFalse,
			"WorkspaceStorageNotReady",
		)
		return ctrl.Result{Requeue: true}, nil
	}

	if !volumeReadyForBuild(volumeRef) {
		reportWorkspaceApplicationBuildStatus(
			buildConfig,
			v1alpha1.WorkspaceApplicationBuildAvailable,
			metav1.ConditionFalse,
			"VolumeNotReadyForBuild",
		)
		return ctrl.Result{Requeue: true}, nil
	}

	jobConfig := imagebuilder.BuildParams{
		JobName:        fmt.Sprintf("%s-build", buildConfig.Name),
		Namespace:      buildConfig.Namespace,
		PVCName:        volumeRef.Status.PvcName,
		Context:        buildConfig.Spec.ContextRef.Context,
		DockerfilePath: buildConfig.Spec.ContextRef.DockerfilePath,
		Registry:       buildConfig.Spec.Registry,
		ImageName:      buildConfig.Spec.ResourceName,
		Tag:            buildConfig.Spec.SourceHash,
		Insecure:       false,
	}

	desiredImageBuilderJob, err := imagebuilder.GenerateImageBuildJob(jobConfig)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := controllerutil.SetControllerReference(buildConfig, desiredImageBuilderJob, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	existingJob := &batchv1.Job{}
	if err := r.Client.Get(
		ctx,
		types.NamespacedName{
			Name:      desiredImageBuilderJob.Name,
			Namespace: desiredImageBuilderJob.Namespace,
		},
		existingJob,
	); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.handleBuildJobCreation(ctx, desiredImageBuilderJob, buildConfig)
		}
		return ctrl.Result{}, err
	}
	JobCompletedCondition := findJobCondition(existingJob, batchv1.JobComplete)
	JobFailedCondition := findJobCondition(existingJob, batchv1.JobFailed)
	if JobCompletedCondition != nil && JobCompletedCondition.Status == v1.ConditionStatus(metav1.ConditionTrue) {
		buildConfig.Status.ImageUrl = jobConfig.ImageUrl()
		reportWorkspaceApplicationBuildComplete(buildConfig)
		return ctrl.Result{}, nil
	}
	if JobFailedCondition != nil && JobFailedCondition.Status == v1.ConditionStatus(metav1.ConditionTrue) {
		reportWorkspaceApplicationBuildStatus(buildConfig, v1alpha1.WorkspaceApplicationBuildFailed, metav1.ConditionTrue, "BuildJobFailed")
		return ctrl.Result{}, nil
	}

	reportWorkspaceApplicationBuildStatus(buildConfig, v1alpha1.WorkspaceApplicationBuildAvailable, metav1.ConditionFalse, "BuildJobNotYetComplete")
	return ctrl.Result{}, nil
}

func (r *WorkspaceApplicationBuildReconciler) handleBuildJobCreation(ctx context.Context, desiredJob *batchv1.Job, buildConfig *v1alpha1.WorkspaceApplicationBuild) error {
	if err := r.Client.Create(ctx, desiredJob); err != nil {
		return err
	}
	buildConfig.Status.BuildSourceHash = buildConfig.Spec.SourceHash
	meta.SetStatusCondition(&buildConfig.Status.Conditions, metav1.Condition{
		Type:    string(v1alpha1.WorkspaceApplicationJobCreated),
		Status:  metav1.ConditionTrue,
		Reason:  "BuildJobCreated",
		Message: "BuildJobCreated",
	})
	return nil
}

func findJobCondition(job *batchv1.Job, jobCondition batchv1.JobConditionType) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		if job.Status.Conditions[i].Type == jobCondition {
			return &job.Status.Conditions[i]
		}
	}
	return nil
}

func volumeAvailable(volume *v1alpha1.WorkspaceVolume) bool {
	cond := meta.FindStatusCondition(volume.Status.Conditions, string(v1alpha1.WorkspaceVolumeConditionAvailable))
	if cond == nil || cond.Status == metav1.ConditionFalse {
		return false
	}
	return true
}

func volumeReadyForBuild(volume *v1alpha1.WorkspaceVolume) bool {
	cond := meta.FindStatusCondition(volume.Status.Conditions, string(v1alpha1.WorkspaceVolumeConditionSyncedOnce))
	if cond == nil || cond.Status == metav1.ConditionFalse {
		return false
	}
	return true
}

func reportWorkspaceApplicationBuildStatus(
	buildConfig *v1alpha1.WorkspaceApplicationBuild,
	condition v1alpha1.WorkspaceApplicationBuildStatusCondition,
	value metav1.ConditionStatus,
	reason string,
) {
	buildConfig.Status.ObservedGeneration = buildConfig.Generation
	buildConfig.Status.BuildSourceHash = buildConfig.Spec.SourceHash
	meta.SetStatusCondition(&buildConfig.Status.Conditions, metav1.Condition{
		Type:               string(condition),
		Status:             value,
		ObservedGeneration: buildConfig.Generation,
		Reason:             reason,
		Message:            reason,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceApplicationBuildReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.WorkspaceApplicationBuild{}).
		Watches(&batchv1.Job{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.WorkspaceApplicationBuild{})).
		Complete(r)
}
