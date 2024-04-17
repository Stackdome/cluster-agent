/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
	workspacev1alpha1 "soradev.io/cluster-agent/api/v1alpha1"
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
	workspaceStorageRef := &v1alpha1.WorkspaceStorage{}

	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      buildConfig.Spec.ContextRef.WorkspaceStorageName,
		Namespace: buildConfig.Namespace,
	}, workspaceStorageRef); err != nil {
		reportWorkspaceApplicationBuildStatus(
			buildConfig, v1alpha1.WorkspaceApplicationBuildAvailable, metav1.ConditionFalse, "WorkspaceStorageFetchError")
		return ctrl.Result{}, err
	}

	if !workspaceStorageAvailable(workspaceStorageRef) {
		reportWorkspaceApplicationBuildStatus(
			buildConfig,
			v1alpha1.WorkspaceApplicationBuildAvailable,
			metav1.ConditionFalse,
			"WorkspaceStorageNotReady",
		)
		return ctrl.Result{Requeue: true}, nil
	}

	if !workspaceStorageReadyForUse(workspaceStorageRef) {
		reportWorkspaceApplicationBuildStatus(
			buildConfig,
			v1alpha1.WorkspaceApplicationBuildAvailable,
			metav1.ConditionFalse,
			"WorkspaceStorageNotReadyForUse",
		)
		return ctrl.Result{Requeue: true}, nil
	}

	resourceStorageReferenced := workspaceStorageRef.ResourceStorageSpec(buildConfig.Spec.ContextRef.ResourceName)

	if resourceStorageReferenced == nil {
		return ctrl.Result{}, fmt.Errorf("resource referenced in the build not in the workspace storage")
	}

	jobConfig := imagebuilder.BuildParams{
		JobName:        buildConfig.Name,
		Namespace:      buildConfig.Namespace,
		PVCName:        workspaceStorageRef.GeneratePVCName(resourceStorageReferenced),
		DockerfilePath: buildConfig.Spec.ContextRef.DockerfilePath,
		Registry:       buildConfig.Spec.Registry,
		// TODO: improve this, using workspaceStateRef.Name here
		ImageName: workspaceStorageRef.Name,
		Tag:       buildConfig.Spec.SourceHash,
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
			return ctrl.Result{}, r.Client.Create(ctx, desiredImageBuilderJob)
		}
		return ctrl.Result{}, err
	}
	JobCompletedCondition := findJobCompleteCondition(existingJob)

	if JobCompletedCondition != nil && JobCompletedCondition.Status == v1.ConditionStatus(metav1.ConditionTrue) {
		buildConfig.Status.ImageUrl = jobConfig.ImageUrl()
		reportWorkspaceApplicationBuildComplete(buildConfig)
		return ctrl.Result{}, nil
	}
	reportWorkspaceApplicationBuildStatus(buildConfig, v1alpha1.WorkspaceApplicationBuildAvailable, metav1.ConditionFalse, "BuildJobNotYetCompleted")
	return ctrl.Result{}, nil
	// TODO: Improve this:
	// - Consider all status conditions.
	// - Refactor this big ass method.

}

func findJobCompleteCondition(job *batchv1.Job) *batchv1.JobCondition {
	for i := range job.Status.Conditions {
		if job.Status.Conditions[i].Type == batchv1.JobComplete {
			return &job.Status.Conditions[i]
		}
	}
	return nil
}

func workspaceStorageAvailable(workspaceStorage *v1alpha1.WorkspaceStorage) bool {
	cond := meta.FindStatusCondition(workspaceStorage.Status.Conditions, string(v1alpha1.WorkspaceStorageAvailable))
	if cond == nil || cond.Status == metav1.ConditionFalse {
		return false
	}
	return true
}

func workspaceStorageReadyForUse(workspaceStorage *v1alpha1.WorkspaceStorage) bool {
	cond := meta.FindStatusCondition(workspaceStorage.Status.Conditions, string(v1alpha1.WorkspaceStorageReadyForUse))
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
		For(&workspacev1alpha1.WorkspaceApplicationBuild{}).
		Watches(&batchv1.Job{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &workspacev1alpha1.WorkspaceApplicationBuild{})).
		Complete(r)
}
