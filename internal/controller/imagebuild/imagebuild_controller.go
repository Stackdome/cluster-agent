package imagebuild

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
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	stackv1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"

	"stackdome.io/cluster-agent/internal/controller"
	"stackdome.io/cluster-agent/pkg/imagebuilder"
)

// ImageBuildReconciler reconciles a ImageBuild object
type ImageBuildReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *ImageBuildReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ctx = controller.ContextWithLogger(ctx, logger.WithValues("ImageBuild", req.String()))
	imageBuild := &buildsv1alpha1.ImageBuild{}

	if err := r.Client.Get(ctx, req.NamespacedName, imageBuild); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	res, err := r.reconcile(ctx, imageBuild)
	if err != nil {
		return res, err
	}
	return res, r.Client.Status().Update(ctx, imageBuild)
}

func (r *ImageBuildReconciler) reconcile(ctx context.Context, buildConfig *buildsv1alpha1.ImageBuild) (ctrl.Result, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconciling image build")
	volumeRef := &storagev1alpha1.Volume{}

	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      buildConfig.Spec.ContextRef.VolumeName,
		Namespace: buildConfig.Namespace,
	}, volumeRef); err != nil {
		return ctrl.Result{}, err
	}

	if !volumeAvailable(volumeRef) {
		reportImageBuildStatus(
			buildConfig,
			buildsv1alpha1.BuildAvailable,
			metav1.ConditionFalse,
			"WorkspaceStorageNotReady",
		)
		return ctrl.Result{Requeue: true}, nil
	}

	if !volumeReadyForBuild(volumeRef) {
		reportImageBuildStatus(
			buildConfig,
			buildsv1alpha1.BuildAvailable,
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
		RegistryURL:    buildConfig.Spec.RegistryURL,
		ImageName:      buildConfig.Spec.ResourceName,
		Tag:            buildConfig.Spec.SourceHash,
		Insecure:       buildConfig.Spec.InsecureRegistry,
	}

	if err := r.configureAuth(ctx, buildConfig, &jobConfig); err != nil {
		return ctrl.Result{}, err
	}

	desiredImageBuilderJob, err := imagebuilder.GenerateImageBuildJob(jobConfig)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := controllerutil.SetControllerReference(buildConfig, desiredImageBuilderJob, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	buildCompletedCondition := meta.FindStatusCondition(buildConfig.Status.Conditions, string(buildsv1alpha1.BuildAvailable))
	if buildCompletedCondition != nil && buildCompletedCondition.Status == metav1.ConditionTrue {
		return ctrl.Result{}, nil
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
		reportImageBuildComplete(buildConfig)
		return ctrl.Result{}, nil
	}
	if JobFailedCondition != nil && JobFailedCondition.Status == v1.ConditionStatus(metav1.ConditionTrue) {
		reportImageBuildStatus(buildConfig, buildsv1alpha1.BuildFailed, metav1.ConditionTrue, "BuildJobFailed")
		return ctrl.Result{}, nil
	}

	reportImageBuildStatus(buildConfig, buildsv1alpha1.BuildAvailable, metav1.ConditionFalse, "BuildJobNotYetComplete")
	return ctrl.Result{}, nil
}

func (r *ImageBuildReconciler) configureAuth(ctx context.Context, buildConfig *buildsv1alpha1.ImageBuild, jobConfig *imagebuilder.BuildParams) error {
	if buildConfig.Spec.Auth == nil {
		return nil
	}

	auth := buildConfig.Spec.Auth
	switch auth.Type {
	case stackv1alpha1.RegistryAuthTypeDockerHub, stackv1alpha1.RegistryAuthTypeInClusterZotRegistry:
		dockerConfigSecret, err := r.getDockerConfigSecret(ctx, auth.DockerAuthSecretRef)
		if err != nil {
			return err
		}
		jobConfig.DockerConfigSecret = dockerConfigSecret
		jobConfig.DockerConfigSecretKey = auth.DockerAuthSecretRef.AuthKey
		return nil
	default:
		return fmt.Errorf("unsupported registry auth type: %s", auth.Type)
	}
}

func (r *ImageBuildReconciler) getDockerConfigSecret(ctx context.Context, secretRef *buildsv1alpha1.DockerAuthSecretRef) (*v1.Secret, error) {
	dockerConfigSecret := &v1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      secretRef.SecretName,
		Namespace: secretRef.SecretNamespace,
	}, dockerConfigSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("docker config secret not found")
		}
		return nil, err
	}
	return dockerConfigSecret, nil
}

func (r *ImageBuildReconciler) handleBuildJobCreation(ctx context.Context, desiredJob *batchv1.Job, buildConfig *buildsv1alpha1.ImageBuild) error {
	if err := r.Client.Create(ctx, desiredJob); err != nil {
		return err
	}
	buildConfig.Status.BuildSourceHash = buildConfig.Spec.SourceHash
	meta.SetStatusCondition(&buildConfig.Status.Conditions, metav1.Condition{
		Type:    string(buildsv1alpha1.BuildJobCreated),
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

func volumeAvailable(volume *storagev1alpha1.Volume) bool {
	cond := meta.FindStatusCondition(volume.Status.Conditions, string(storagev1alpha1.VolumeConditionAvailable))
	if cond == nil || cond.Status == metav1.ConditionFalse {
		return false
	}
	return true
}

func volumeReadyForBuild(volume *storagev1alpha1.Volume) bool {
	cond := meta.FindStatusCondition(volume.Status.Conditions, string(storagev1alpha1.VolumeConditionSyncedOnce))
	if cond == nil || cond.Status == metav1.ConditionFalse {
		return false
	}
	return true
}

func reportImageBuildStatus(
	buildConfig *buildsv1alpha1.ImageBuild,
	condition buildsv1alpha1.BuildStatusCondition,
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
	buildConfig.Status.StatusHash = buildConfig.StatusHash()
}

func reportImageBuildComplete(buildConfig *buildsv1alpha1.ImageBuild) {
	buildConfig.Status.Phase = buildsv1alpha1.BuildPhaseSuccess
	buildConfig.Status.BuildSourceHash = buildConfig.Spec.SourceHash
	meta.SetStatusCondition(&buildConfig.Status.Conditions, metav1.Condition{
		Type:               string(buildsv1alpha1.BuildAvailable),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: buildConfig.Generation,
		Reason:             "BuildComplete",
		Message:            "Image build compelete",
	})
	buildConfig.Status.StatusHash = buildConfig.StatusHash()
}

// SetupWithManager sets up the controller with the Manager.
func (r *ImageBuildReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&buildsv1alpha1.ImageBuild{}).
		Watches(&batchv1.Job{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &buildsv1alpha1.ImageBuild{})).
		Complete(r)
}
