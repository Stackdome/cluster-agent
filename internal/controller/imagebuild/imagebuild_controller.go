package imagebuild

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"
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
	"stackdome.io/cluster-agent/pkg/registry"
)

// ImageBuildReconciler reconciles a ImageBuild object
type ImageBuildReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	UncachedClient client.Client
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

	if imageBuildInTerminalState(buildConfig) {
		return ctrl.Result{}, nil
	}

	if buildConfig.Spec.Cancelled {
		return r.reconcileCancellation(ctx, buildConfig)
	}

	switch {
	case buildConfig.Spec.BuildContext.ContextSource.Git != nil:
		return r.reconcileImageBuildWithGitSource(ctx, buildConfig)
	case buildConfig.Spec.BuildContext.ContextSource.Volume != nil:
		return r.reconcileImageBuildWithVolumeSource(ctx, buildConfig)
	default:
		logger.Info("no context source specified for image build")
		return ctrl.Result{}, fmt.Errorf("no context source specified for image build")
	}
}

func (r *ImageBuildReconciler) reconcileCancellation(ctx context.Context, buildConfig *buildsv1alpha1.ImageBuild) (ctrl.Result, error) {
	logger := controller.LoggerFromContext(ctx)

	var jobs batchv1.JobList
	if err := r.Client.List(ctx, &jobs, client.InNamespace(buildConfig.Namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list Jobs for cancellation: %w", err)
	}

	var errGroup errgroup.Group
	for i := range jobs.Items {
		job := &jobs.Items[i]
		if !metav1.IsControlledBy(job, buildConfig) {
			continue
		}

		errGroup.Go(func() error {
			logger.Info("deleting build job for cancelled ImageBuild", "job", job.Name)
			if err := r.Client.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground)); err != nil &&
				!apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete build job %s: %w", job.Name, err)
			}
			return nil
		})
	}

	buildConfig.Status.Phase = buildsv1alpha1.BuildPhaseCancelled
	reportImageBuildStatus(buildConfig, buildsv1alpha1.BuildCancelled, metav1.ConditionTrue, "BuildCancelled")
	return ctrl.Result{}, errGroup.Wait()
}

func (r *ImageBuildReconciler) reconcileImageBuildWithVolumeSource(ctx context.Context, buildConfig *buildsv1alpha1.ImageBuild) (ctrl.Result, error) {
	if buildConfig.Spec.BuildContext.ContextSource.Volume == nil {
		return ctrl.Result{}, fmt.Errorf("no volume source specified for image build")
	}
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconciling image build with volume source")

	volumeRef := &storagev1alpha1.Volume{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      buildConfig.Spec.BuildContext.ContextSource.Volume.Name,
		Namespace: buildConfig.Namespace,
	}, volumeRef); err != nil {
		return ctrl.Result{}, err
	}

	if !volumeAvailable(volumeRef) {
		reportImageBuildStatus(
			buildConfig,
			buildsv1alpha1.BuildAvailable,
			metav1.ConditionFalse,
			"VolumeNotReady",
		)
		return ctrl.Result{Requeue: true}, nil
	}

	if !volumeReadyForBuild(volumeRef, buildConfig) {
		reportImageBuildStatus(
			buildConfig,
			buildsv1alpha1.BuildAvailable,
			metav1.ConditionFalse,
			"VolumeNotReadyForBuild",
		)
		return ctrl.Result{Requeue: true}, nil
	}

	dockerSecret, dockerSecretKey, err := r.getRegistryAuthSecrets(ctx, buildConfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	resolvedBuildArgs, err := r.resolveBuildArgs(ctx, buildConfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	sourceRevision := buildConfig.Spec.SourceRevision.GetSourceRevisionString()
	jobName := buildsv1alpha1.BuildJobName(buildConfig.Spec.ResourceName, sourceRevision)
	buildSource := &imagebuilder.Source{
		Volume: &imagebuilder.VolumeSource{
			PvcName: volumeRef.Status.PvcName,
			// If the volume is synced from a git repo, we need to set the git repo path.
			// For non git synced volumes, this will be empty.
			GitRepoPath: volumeRef.Status.GitRepoSyncedPathWithinVolume,
		},
	}

	resolved, err := r.resolveDestination(ctx, buildConfig)
	if err != nil {
		reportImageBuildStatus(buildConfig, buildsv1alpha1.BuildAvailable, metav1.ConditionFalse, "RepositoryResolveFailed")
		return ctrl.Result{}, err
	}

	imageBuilderParams := imagebuilder.
		NewBuildParamsBuilder().
		WithJobName(jobName).
		WithNamespace(buildConfig.Namespace).
		WithDestination(resolved.Reference()).
		WithInsecureRegistry(resolved.Insecure).
		WithDockerfilePath(buildConfig.Spec.BuildContext.DockerfilePath).
		WithContextPath(buildConfig.Spec.BuildContext.ContextPath).
		WithSource(buildSource).
		WithDockerAuth(dockerSecret, dockerSecretKey).
		WithBuildArgs(resolvedBuildArgs).
		Build()

	desiredImageBuilderJob, err := imagebuilder.GenerateImageBuildJob(imageBuilderParams)
	if err != nil {
		return ctrl.Result{}, err
	}
	setBuildJobAnnotations(desiredImageBuilderJob, buildConfig.Spec.ResourceName, sourceRevision, buildConfig.Spec.BuildContext.DockerfilePath)
	if err := controllerutil.SetControllerReference(buildConfig, desiredImageBuilderJob, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	return r.reconcileBuildJob(ctx, buildConfig, desiredImageBuilderJob, imageBuilderParams)
}

func (r *ImageBuildReconciler) reconcileImageBuildWithGitSource(ctx context.Context, buildConfig *buildsv1alpha1.ImageBuild) (ctrl.Result, error) {
	if buildConfig.Spec.BuildContext.ContextSource.Git == nil {
		return ctrl.Result{}, fmt.Errorf("no git source specified for image build")
	}

	if err := validateGitRevision(buildConfig.Spec.SourceRevision.GitRepo); err != nil {
		reportImageBuildStatus(buildConfig, buildsv1alpha1.BuildFailed, metav1.ConditionTrue, "SourceRevisionInvalid")
		buildConfig.Status.Phase = buildsv1alpha1.BuildPhaseFailed
		return ctrl.Result{}, nil
	}

	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconciling image build with git source")

	dockerSecret, dockerSecretKey, err := r.getRegistryAuthSecrets(ctx, buildConfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	resolvedBuildArgs, err := r.resolveBuildArgs(ctx, buildConfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	sourceRevision := buildConfig.Spec.SourceRevision.GetSourceRevisionString()
	jobName := buildsv1alpha1.BuildJobName(buildConfig.Spec.ResourceName, sourceRevision)
	buildSource := &imagebuilder.Source{
		GitRepo: &imagebuilder.GitRepoBuildSource{
			Repo:     buildConfig.Spec.BuildContext.ContextSource.Git.DeepCopy(),
			Revision: buildConfig.Spec.SourceRevision.GitRepo.DeepCopy(),
		},
	}

	resolved, err := r.resolveDestination(ctx, buildConfig)
	if err != nil {
		reportImageBuildStatus(buildConfig, buildsv1alpha1.BuildAvailable, metav1.ConditionFalse, "RepositoryResolveFailed")
		return ctrl.Result{}, err
	}

	imageBuilderParams := imagebuilder.
		NewBuildParamsBuilder().
		WithJobName(jobName).
		WithNamespace(buildConfig.Namespace).
		WithDestination(resolved.Reference()).
		WithInsecureRegistry(resolved.Insecure).
		WithDockerfilePath(buildConfig.Spec.BuildContext.DockerfilePath).
		WithContextPath(buildConfig.Spec.BuildContext.ContextPath).
		WithSource(buildSource).
		WithDockerAuth(dockerSecret, dockerSecretKey).
		WithBuildArgs(resolvedBuildArgs).
		Build()

	desiredImageBuilderJob, err := imagebuilder.GenerateImageBuildJob(imageBuilderParams)
	if err != nil {
		return ctrl.Result{}, err
	}
	setBuildJobAnnotations(desiredImageBuilderJob, buildConfig.Spec.ResourceName, sourceRevision, buildConfig.Spec.BuildContext.DockerfilePath)
	if err := controllerutil.SetControllerReference(buildConfig, desiredImageBuilderJob, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	return r.reconcileBuildJob(ctx, buildConfig, desiredImageBuilderJob, imageBuilderParams)
}

func (r *ImageBuildReconciler) reconcileBuildJob(ctx context.Context, buildConfig *buildsv1alpha1.ImageBuild, desiredJob *batchv1.Job, params imagebuilder.BuildParams) (ctrl.Result, error) {
	logger := controller.LoggerFromContext(ctx)

	existingJob := &batchv1.Job{}
	if err := r.Client.Get(
		ctx,
		types.NamespacedName{
			Name:      desiredJob.Name,
			Namespace: desiredJob.Namespace,
		},
		existingJob,
	); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.handleBuildJobCreation(ctx, desiredJob, buildConfig)
		}
		return ctrl.Result{}, err
	}

	jobCompletedCondition := findJobCondition(existingJob, batchv1.JobComplete)
	jobFailedCondition := findJobCondition(existingJob, batchv1.JobFailed)

	if jobCompletedCondition != nil && jobCompletedCondition.Status == v1.ConditionStatus(metav1.ConditionTrue) {
		buildConfig.Status.ImageUrl = params.ImageUrl()
		reportImageBuildComplete(buildConfig)
		return ctrl.Result{}, nil
	}

	if jobFailedCondition != nil && jobFailedCondition.Status == v1.ConditionStatus(metav1.ConditionTrue) {
		// Capture final failure details, but don't overwrite existing details if
		// pods have been cleaned up (podReplacementPolicy: TerminatingOrFailed
		// removes pods before the Job transitions to Failed).
		buildFailureDetail, err := getBuildFailureDetail(ctx, r.UncachedClient, buildConfig, existingJob)
		if err != nil {
			logger.Error(err, fmt.Sprintf("failed to capture build failure detail for job %s", existingJob.Name))
		} else if buildFailureDetail != nil {
			buildConfig.Status.LastBuildFailureDetail = buildFailureDetail
		}
		reportImageBuildStatus(buildConfig, buildsv1alpha1.BuildFailed, metav1.ConditionTrue, "BuildJobFailed")
		buildConfig.Status.Phase = buildsv1alpha1.BuildPhaseFailed
		return ctrl.Result{}, nil
	}

	// Job still running — capture intermediate failures (best effort).
	// Skip if we've already captured details to avoid redundant pod listing.
	if buildConfig.Status.LastBuildFailureDetail == nil {
		buildFailureDetail, err := getBuildFailureDetail(ctx, r.UncachedClient, buildConfig, existingJob)
		if err != nil {
			logger.Error(err, fmt.Sprintf("failed to capture build failure detail for job %s", existingJob.Name))
		} else {
			buildConfig.Status.LastBuildFailureDetail = buildFailureDetail
		}
	}

	reportImageBuildStatus(buildConfig, buildsv1alpha1.BuildAvailable, metav1.ConditionFalse, "BuildJobNotYetComplete")
	return ctrl.Result{}, nil
}

func (r *ImageBuildReconciler) resolveDestination(ctx context.Context, bc *buildsv1alpha1.ImageBuild) (registry.ResolvedRepository, error) {
	return registry.ResolveImageRepository(ctx, r.Client, bc.Namespace,
		bc.Spec.Repository, bc.Spec.SourceRevision.GetSourceRevisionString())
}

func (r *ImageBuildReconciler) getRegistryAuthSecrets(ctx context.Context, buildConfig *buildsv1alpha1.ImageBuild) (*v1.Secret, string, error) {
	if buildConfig.Spec.Repository.Auth == nil {
		return nil, "", nil
	}

	auth := buildConfig.Spec.Repository.Auth
	switch {
	case auth.DockerConfig != nil:
		secret, err := r.getDockerConfigSecret(ctx, auth.DockerConfig)
		if err != nil {
			return nil, "", err
		}
		return secret, auth.DockerConfig.SecretKey, nil
	case auth.Basic != nil:
		secretName := stackv1alpha1.SynthesizedDockerConfigSecretName(buildConfig.Spec.ResourceName)
		secret := &v1.Secret{}
		if err := r.Client.Get(ctx, types.NamespacedName{
			Name:      secretName,
			Namespace: buildConfig.Namespace,
		}, secret); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, "", fmt.Errorf("synthesized docker config secret %q not found", secretName)
			}
			return nil, "", err
		}
		return secret, v1.DockerConfigJsonKey, nil
	default:
		return nil, "", nil
	}
}

func (r *ImageBuildReconciler) getDockerConfigSecret(ctx context.Context, dockerConfigAuth *stackv1alpha1.DockerConfigAuth) (*v1.Secret, error) {
	dockerConfigSecret := &v1.Secret{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      dockerConfigAuth.SecretRef.Name,
		Namespace: dockerConfigAuth.SecretRef.Namespace,
	}, dockerConfigSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("docker config secret not found")
		}
		return nil, err
	}
	return dockerConfigSecret, nil
}

func (r *ImageBuildReconciler) resolveBuildArgs(ctx context.Context, buildConfig *buildsv1alpha1.ImageBuild) ([]imagebuilder.ResolvedBuildArg, error) {
	if len(buildConfig.Spec.BuildArgs) == 0 {
		return nil, nil
	}

	resolved := make([]imagebuilder.ResolvedBuildArg, 0, len(buildConfig.Spec.BuildArgs))
	for _, arg := range buildConfig.Spec.BuildArgs {
		if arg.ValueFrom != nil {
			secret := &v1.Secret{}
			if err := r.Client.Get(ctx, types.NamespacedName{
				Name:      arg.ValueFrom.SecretKeyRef.Name,
				Namespace: buildConfig.Namespace,
			}, secret); err != nil {
				if apierrors.IsNotFound(err) {
					reportImageBuildStatus(buildConfig, buildsv1alpha1.BuildFailed, metav1.ConditionTrue, "BuildArgSecretNotFound")
					return nil, fmt.Errorf("secret %q not found for build arg %q", arg.ValueFrom.SecretKeyRef.Name, arg.Name)
				}
				return nil, fmt.Errorf("failed to get secret for build arg %q: %w", arg.Name, err)
			}
			val, ok := secret.Data[arg.ValueFrom.SecretKeyRef.Key]
			if !ok {
				reportImageBuildStatus(buildConfig, buildsv1alpha1.BuildFailed, metav1.ConditionTrue, "BuildArgSecretKeyNotFound")
				return nil, fmt.Errorf("key %q not found in secret %q for build arg %q", arg.ValueFrom.SecretKeyRef.Key, arg.ValueFrom.SecretKeyRef.Name, arg.Name)
			}
			resolved = append(resolved, imagebuilder.ResolvedBuildArg{Name: arg.Name, Value: string(val)})
		} else {
			resolved = append(resolved, imagebuilder.ResolvedBuildArg{Name: arg.Name, Value: arg.Value})
		}
	}
	return resolved, nil
}

func (r *ImageBuildReconciler) handleBuildJobCreation(ctx context.Context, desiredJob *batchv1.Job, buildConfig *buildsv1alpha1.ImageBuild) error {
	if err := r.Client.Create(ctx, desiredJob); err != nil {
		return err
	}
	buildConfig.Status.BuildSourceRevision = buildConfig.Spec.SourceRevision.GetSourceRevisionString()
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

func volumeReadyForBuild(volume *storagev1alpha1.Volume, imageBuild *buildsv1alpha1.ImageBuild) bool {
	volumeSource := volume.Spec.Source
	if volumeSource == nil {
		return false
	}

	if volumeSource.RemoteDir != nil {
		cond := meta.FindStatusCondition(volume.Status.Conditions, string(storagev1alpha1.VolumeConditionSyncedFromRemote))
		if cond == nil || cond.Status == metav1.ConditionFalse || cond.ObservedGeneration != volume.Generation {
			return false
		}
		// Check if the local directory hash matches the image build source revision
		return volumeSource.RemoteDir.CurrentDirectoryHash == imageBuild.Spec.SourceRevision.GetSourceRevisionString()
	} else if volumeSource.GitRepo != nil {
		cond := meta.FindStatusCondition(volume.Status.Conditions, string(storagev1alpha1.VolumeConditionSyncedFromGitSource))
		if cond == nil || cond.Status == metav1.ConditionFalse || cond.ObservedGeneration != volume.Generation {
			return false
		}
		// Check if the git reference matches the image build source revision
		return volumeSource.GitRepo.Revision.GetGitRevisionString() == imageBuild.Spec.SourceRevision.GetSourceRevisionString()
	}
	return false
}

func reportImageBuildStatus(
	buildConfig *buildsv1alpha1.ImageBuild,
	condition buildsv1alpha1.BuildStatusCondition,
	value metav1.ConditionStatus,
	reason string,
) {
	buildConfig.Status.ObservedGeneration = buildConfig.Generation
	buildConfig.Status.BuildSourceRevision = buildConfig.Spec.SourceRevision.GetSourceRevisionString()
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
	buildConfig.Status.LastBuildFailureDetail = nil
	buildConfig.Status.BuildSourceRevision = buildConfig.Spec.SourceRevision.GetSourceRevisionString()
	meta.SetStatusCondition(&buildConfig.Status.Conditions, metav1.Condition{
		Type:               string(buildsv1alpha1.BuildAvailable),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: buildConfig.Generation,
		Reason:             "BuildComplete",
		Message:            "Image build complete",
	})
	buildConfig.Status.StatusHash = buildConfig.StatusHash()
}

func setBuildJobAnnotations(job *batchv1.Job, resourceName, sourceRevision, dockerfilePath string) {
	if job.Annotations == nil {
		job.Annotations = make(map[string]string)
	}
	job.Annotations[buildsv1alpha1.AnnotationResourceName] = resourceName
	job.Annotations[buildsv1alpha1.AnnotationSourceRevision] = sourceRevision
	job.Annotations[buildsv1alpha1.AnnotationDockerfilePath] = dockerfilePath
}

func validateGitRevision(rev *stackv1alpha1.GitRepoRevision) error {
	if rev == nil {
		return fmt.Errorf("git source revision is required")
	}
	commit := strings.TrimSpace(strings.ToLower(rev.Commit))
	if commit == "" || commit == "head" {
		return fmt.Errorf("git source revision must include a concrete commit SHA, got %q", rev.Commit)
	}
	hasFetchRef := rev.Branch != "" || rev.Tag != ""
	if !hasFetchRef {
		return fmt.Errorf("git source revision must include a branch or tag as a fetchable ref")
	}
	return nil
}

func imageBuildInTerminalState(buildConfig *buildsv1alpha1.ImageBuild) bool {
	availableCond := meta.FindStatusCondition(buildConfig.Status.Conditions, string(buildsv1alpha1.BuildAvailable))
	if availableCond != nil && availableCond.Status == metav1.ConditionTrue {
		return true
	}

	failedCond := meta.FindStatusCondition(buildConfig.Status.Conditions, string(buildsv1alpha1.BuildFailed))
	if failedCond != nil && failedCond.Status == metav1.ConditionTrue {
		return true
	}

	cancelledCond := meta.FindStatusCondition(buildConfig.Status.Conditions, string(buildsv1alpha1.BuildCancelled))
	if cancelledCond != nil && cancelledCond.Status == metav1.ConditionTrue {
		return true
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *ImageBuildReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&buildsv1alpha1.ImageBuild{}).
		Watches(&batchv1.Job{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &buildsv1alpha1.ImageBuild{})).
		Complete(r)
}
