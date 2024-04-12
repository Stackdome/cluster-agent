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

func (r *WorkspaceApplicationBuildReconciler) reconcile(ctx context.Context, buildConfig *v1alpha1.WorkspaceApplicationBuild) (ctrl.Result, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconciling application build")
	workspaceStateRef := &v1alpha1.WorkspaceState{}

	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      buildConfig.Spec.ContextRef.WorkspaceStateName,
		Namespace: buildConfig.Namespace,
	}, workspaceStateRef); err != nil {
		reportWorkspaceApplicationBuildStatus(
			buildConfig, v1alpha1.WorkspaceApplicationBuildAvailable, metav1.ConditionFalse, "WorkspaceStateFetchError")
		return ctrl.Result{}, err
	}

	if !workspaceStateAvailable(workspaceStateRef) {
		reportWorkspaceApplicationBuildStatus(
			buildConfig,
			v1alpha1.WorkspaceApplicationBuildAvailable,
			metav1.ConditionFalse,
			"WorkspaceStateNotReady",
		)
		return ctrl.Result{Requeue: true}, nil
	}
	workSpaceResourceReferenced := func() *v1alpha1.WorkspaceResourceStorage {
		for i := range workspaceStateRef.Spec.Resources {
			curr := &workspaceStateRef.Spec.Resources[i]
			if curr.Name == buildConfig.Spec.ContextRef.ResourceName {
				return curr
			}
		}
		return nil
	}()

	if workSpaceResourceReferenced == nil {
		return ctrl.Result{}, fmt.Errorf("resource referenced in the build not in the workspace state")
	}

	jobConfig := imagebuilder.BuildParams{
		JobName:        workspaceStateRef.Name,
		Namespace:      buildConfig.Namespace,
		PVCName:        workSpaceResourceReferenced.Name,
		DockerfilePath: buildConfig.Spec.ContextRef.DockerfilePath,
		Registry:       buildConfig.Spec.Registry,
		// TODO: improve this, using workspaceStateRef.Name here
		ImageName: workspaceStateRef.Name,
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
	if JobCompletedCondition == nil || JobCompletedCondition.Status == v1.ConditionStatus(metav1.ConditionFalse) {
		if JobCompletedCondition != nil {
			reportWorkspaceApplicationBuildStatus(buildConfig, v1alpha1.WorkspaceApplicationBuildAvailable, metav1.ConditionFalse, JobCompletedCondition.Reason)
			return ctrl.Result{}, nil
		}
		reportWorkspaceApplicationBuildStatus(buildConfig, v1alpha1.WorkspaceApplicationBuildAvailable, metav1.ConditionFalse, "BuildJobNotYetCompleted")
		return ctrl.Result{}, nil
	}
	// TODO: Improve this:
	// - Consider all status conditions.
	// - Refactor this big ass method.
	reportWorkspaceApplicationBuildStatus(buildConfig, v1alpha1.WorkspaceApplicationBuildAvailable, metav1.ConditionTrue, "BuildSuccess")
	buildConfig.Status.Phase = v1alpha1.WorkspaceApplicationBuildPhaseSuccess
	buildConfig.Status.ImageUrl = jobConfig.ImageUrl()
	buildConfig.Status.BuildSourceHash = buildConfig.Spec.SourceHash
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

func workspaceStateAvailable(workspaceState *v1alpha1.WorkspaceState) bool {
	cond := meta.FindStatusCondition(workspaceState.Status.Conditions, string(v1alpha1.WorkspaceStateConditionAvailable))
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
		Owns(&batchv1.Job{}).
		Complete(r)
}
