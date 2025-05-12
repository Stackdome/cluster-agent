package stackresource

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
	"stackdome.io/cluster-agent/pkg/interpolation"
)

type DependencyChecker interface {
	DependenciesAvailable(ctx context.Context, resource *v1alpha1.StackResource) (bool, string, error)
	VolumeMountsReadyForUse(ctx context.Context, resource *v1alpha1.StackResource) (bool, string, error)
}

type workloadReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	DependencyChecker DependencyChecker
}

func GetDeploymentNameForResource(resource *v1alpha1.StackResource) string {
	return resource.Name
}

func GetDeploymentPodLabelForResource(resource *v1alpha1.StackResource) map[string]string {
	return map[string]string{
		"resource": GetDeploymentNameForResource(resource),
	}
}

func (r *workloadReconciler) getImageBuild(ctx context.Context, resource *v1alpha1.StackResource) (*buildsv1alpha1.ImageBuild, error) {
	existingApplicationBuild := &buildsv1alpha1.ImageBuild{}
	if err := r.Client.Get(ctx,
		types.NamespacedName{
			Name:      buildsv1alpha1.ImageBuildName(resource.Name, resource.Spec.BuildSpec.SourceRevision.GetSourceRevisionString()),
			Namespace: resource.Namespace,
		},
		existingApplicationBuild,
	); err != nil {
		return nil, err
	}
	return existingApplicationBuild, nil
}

func (r *workloadReconciler) getImageForResource(ctx context.Context, resource *v1alpha1.StackResource) (*string, error) {
	if resource.Spec.BuildSpec != nil {
		requiredBuild, err := r.getImageBuild(ctx, resource)
		if err != nil {
			return nil, err
		}
		return ptr.To(requiredBuild.Status.ImageUrl), nil
	}
	return ptr.To(resource.Spec.ImageSpec.Image), nil
}

func (r *workloadReconciler) reconcile(ctx context.Context, resource *v1alpha1.StackResource) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("in workload reconciler for")
	logger.Info(resource.Name)

	canRun, message, err := r.DependencyChecker.DependenciesAvailable(ctx, resource)
	if err != nil {
		return resultNil, err
	}
	if !canRun {
		// Our dependencies are not yet ready, we will run when our dependencies are available.
		reportStackResourceNotReady(resource, "DependenciesNotReady", message)
		// We need to requeue this request because we dont get requeued automatically when the other dependencies are
		// ready/updated.
		return resultRequeueAfter(DefaultRequeueTime), nil
	}

	volumeMountsReady, message, err := r.DependencyChecker.VolumeMountsReadyForUse(ctx, resource)
	if err != nil {
		logger.Error(err, "failed to check if volume mounts are ready for use")
	}
	if !volumeMountsReady {
		logger.Info("volume mounts are not ready for use")
		reportStackResourceNotReady(resource, "VolumeMountsNotReady", message)
		return resultRequeueAfter(DefaultRequeueTime), nil
	}

	if resource.Spec.BuildSpec != nil {
		currentApplicationBuild, err := r.getImageBuild(ctx, resource)
		if err != nil {
			return resultNil, err
		}

		if !imageBuildComplete(currentApplicationBuild) {
			reportStackResourceNotReady(resource, "ApplicationBuildNotYetReady", "Application build is not yet ready")
			return resultStop, nil
		}
	}

	volumeMountInfo, err := r.getVolumeMountInfoMap(ctx, resource)
	if err != nil {
		return resultNil, err
	}

	siblings, err := r.GetSiblings(ctx, resource)
	if err != nil {
		return resultNil, err
	}

	image, err := r.getImageForResource(ctx, resource)
	if err != nil {
		return resultNil, err
	}

	interpolatedEnvVars, err := r.interpolateEnvVars(resource, siblings)
	if err != nil {
		return resultNil, fmt.Errorf("failed to interpolate env vars: %w", err)
	}

	desiredDeploymentForResource := &appsv1.Deployment{
		ObjectMeta: v1.ObjectMeta{
			Name:      GetDeploymentNameForResource(resource),
			Namespace: resource.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &v1.LabelSelector{
				MatchLabels: GetDeploymentPodLabelForResource(resource),
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: GetDeploymentPodLabelForResource(resource),
				},
				Spec: corev1.PodSpec{
					Volumes: InterpolatedVolumesList(resource, volumeMountInfo),
					Containers: []corev1.Container{
						{
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            resource.Name,
							Image:           *image,
							Command:         resource.Spec.Command,
							Args:            resource.Spec.Args,
							Ports:           InterpolatedContainerPorts(resource),
							Env:             interpolatedEnvVars,
							VolumeMounts:    InterpolatedVolumeMountList(resource),
						},
					},
				},
			},
		},
	}

	if err := r.setImagePullSecret(ctx, resource, desiredDeploymentForResource); err != nil {
		return resultNil, err
	}

	if resource.Spec.Init != nil {
		desiredDeploymentForResource.Spec.Template.Spec.InitContainers = []corev1.Container{
			{
				ImagePullPolicy: corev1.PullIfNotPresent,
				Name:            fmt.Sprintf("%s-init", resource.Name),
				Image:           *image,
				Command:         resource.Spec.Init.Command,
				Args:            resource.Spec.Init.Args,
				Env:             interpolatedEnvVars,
				VolumeMounts:    InterpolatedVolumeMountList(resource),
			},
		}
	}

	if err := controllerutil.SetControllerReference(resource, desiredDeploymentForResource, r.Scheme); err != nil {
		return resultNil, err
	}

	existingDeployment := &appsv1.Deployment{}
	if err := r.Client.Get(ctx,
		types.NamespacedName{
			Name:      desiredDeploymentForResource.Name,
			Namespace: desiredDeploymentForResource.Namespace,
		},
		existingDeployment,
	); err != nil {
		if apierrors.IsNotFound(err) {
			return resultStop, r.Client.Create(ctx, desiredDeploymentForResource)
		}
		return resultNil, err
	}
	if !equality.Semantic.DeepDerivative(desiredDeploymentForResource.DeepCopy().Spec.Template, existingDeployment.DeepCopy().Spec.Template) {
		logger.Info("Updating existing deployment for workload")
		existingDeployment.Spec = desiredDeploymentForResource.Spec
		return resultStop, r.Client.Update(ctx, existingDeployment)
	}

	if r.requiresRestart(resource) {
		logger.Info("restarting deployment for workload")
		if existingDeployment.Spec.Template.Annotations == nil {
			existingDeployment.Spec.Template.Annotations = make(map[string]string)
		}
		existingDeployment.Spec.Template.Annotations[v1alpha1.RestartResourceAnnotation] = v1.Now().UTC().String()
		resource.Status.LastRestartRequestProcessedAt = ptr.To(v1.NewTime(time.Now().UTC()))
		reportStackResourceNotReady(resource, "WorkspaceResouceDeploymentNotReady", "WorkspaceResouceDeploymentNotReady")
		return resultStop, r.Client.Update(ctx, existingDeployment)
	}

	if controller.DeploymentAvailable(existingDeployment) {
		return resultNil, nil
	}
	logger.Info("deployment not ready")
	reportStackResourceNotReady(resource, "WorkspaceResouceDeploymentNotReady", "WorkspaceResouceDeploymentNotReady")
	return resultStop, nil
}

func (r *workloadReconciler) setImagePullSecret(ctx context.Context, resource *v1alpha1.StackResource, deployment *appsv1.Deployment) error {
	if resource.NeedsPullSecret() {
		authUrl, err := resource.RegistryAuthUrl()
		if err != nil {
			return fmt.Errorf("failed to get registry auth url: %w", err)
		}
		authType := resource.RegistryAuthType()
		switch authType {
		case v1alpha1.RegistryAuthTypeDockerHub, v1alpha1.RegistryAuthTypeInClusterZotRegistry:
			dockerConfigSecret := &corev1.Secret{}
			if err := r.Client.Get(ctx, types.NamespacedName{
				Name:      registrySecretName(authUrl),
				Namespace: resource.Namespace,
			}, dockerConfigSecret); err != nil {
				if apierrors.IsNotFound(err) {
					return fmt.Errorf("docker config secret not found: %w", err)
				}
				return fmt.Errorf("failed to get docker config secret: %w", err)
			}
			deployment.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{
				{
					Name: registrySecretName(authUrl),
				},
			}
		default:
			return fmt.Errorf("unsupported registry auth type: %s", authType)
		}
	}
	return nil
}

func (r *workloadReconciler) requiresRestart(resource *v1alpha1.StackResource) bool {
	lastRestartProcessedAt := resource.Status.LastRestartRequestProcessedAt
	currentRestartRequest := resource.Spec.RestartRequest
	switch {
	case currentRestartRequest != nil && lastRestartProcessedAt == nil:
		return true
	case currentRestartRequest != nil && currentRestartRequest.UTC().After(lastRestartProcessedAt.Time.UTC()):
		return true
	default:
		return false
	}
}

func (r *workloadReconciler) interpolateEnvVars(resource *v1alpha1.StackResource, siblings []*v1alpha1.StackResource) ([]corev1.EnvVar, error) {
	interpolationCtx, err := interpolation.NewInterpolationContext(siblings)
	if err != nil {
		return nil, fmt.Errorf("failed to create interpolation context: %w", err)
	}

	interpolator := interpolation.NewInterpolator(interpolationCtx)

	res := make([]corev1.EnvVar, 0)
	for _, env := range resource.Spec.EnvironmentVariables {
		interpolatedValue, err := interpolator.InterpolateString(env.Value)
		if err != nil {
			return nil, fmt.Errorf("failed to interpolate env var '%s': %w", env.Name, err)
		}
		res = append(res, corev1.EnvVar{
			Name:  env.Name,
			Value: interpolatedValue,
		})
	}

	return res, nil
}

func InterpolatedContainerPorts(resource *v1alpha1.StackResource) []corev1.ContainerPort {
	res := make([]corev1.ContainerPort, 0)
	for _, port := range resource.Spec.Ports {
		res = append(res, corev1.ContainerPort{
			ContainerPort: port.Number,
		})
	}
	return res
}

func splitEnvVarValue(value string) (string, string) {
	parts := strings.SplitN(value, ".", 2)
	part1 := strings.TrimPrefix(parts[0], "$")
	return part1, parts[1]
}

func InterpolatedVolumeMountList(resource *v1alpha1.StackResource) []corev1.VolumeMount {
	res := make([]corev1.VolumeMount, 0)
	for _, mount := range resource.Spec.VolumeMounts {
		sourceVolumeName := mount.SourceVolume
		subPath := mount.SourceSubPath
		if len(subPath) == 0 {
			res = append(res, corev1.VolumeMount{
				Name:      sourceVolumeName,
				MountPath: mount.Destination,
			})
		} else {
			res = append(res, corev1.VolumeMount{
				Name:      sourceVolumeName,
				MountPath: mount.Destination,
				SubPath:   strings.TrimPrefix(subPath, "/"),
			})
		}
	}
	return res
}

func InterpolatedVolumesList(resource *v1alpha1.StackResource, volumeInfo map[string]*storagev1alpha1.Volume) []corev1.Volume {
	res := make([]corev1.Volume, 0)
	addedVolumes := make(map[string]struct{})
	for _, mount := range resource.Spec.VolumeMounts {
		sourceVolumeName := mount.SourceVolume
		_, added := addedVolumes[sourceVolumeName]
		if !added {
			res = append(res, corev1.Volume{
				Name: sourceVolumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: volumeInfo[sourceVolumeName].Status.PvcName,
					},
				},
			})
			addedVolumes[sourceVolumeName] = struct{}{}
		}
	}
	return res
}

func (r *workloadReconciler) getVolumeMountInfoMap(ctx context.Context, resource *v1alpha1.StackResource) (map[string]*storagev1alpha1.Volume, error) {
	res := make(map[string]*storagev1alpha1.Volume)
	for _, mount := range resource.Spec.VolumeMounts {
		sourceVolumeName := mount.SourceVolume
		referencedVolume := &storagev1alpha1.Volume{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: sourceVolumeName, Namespace: resource.Namespace}, referencedVolume); err != nil {
			return nil, fmt.Errorf("failed to get the referenced volume '%s' in resource '%s': %w", sourceVolumeName, resource.Name, err)
		}
		res[sourceVolumeName] = referencedVolume
	}
	return res, nil
}

func workspaceResourceName(workspaceName, resourceName string) string {
	return fmt.Sprintf("%s-%s", workspaceName, resourceName)
}

func (r *workloadReconciler) GetSiblings(ctx context.Context, resource *v1alpha1.StackResource) ([]*v1alpha1.StackResource, error) {
	srList := &v1alpha1.StackResourceList{}
	if err := r.Client.List(ctx, srList, client.InNamespace(resource.Namespace)); err != nil {
		return nil, err
	}

	// Set current resource's internal service name
	for i := range srList.Items {
		if srList.Items[i].Name == resource.Name {
			// We do this because we need to pass the all siblings as interpolation context while interpolating the env vars.
			srList.Items[i].Status.InternalAddress = ptr.To(ResourceSVCName(resource))
		}
	}
	res := make([]*v1alpha1.StackResource, len(srList.Items))
	for i := range srList.Items {
		res[i] = &srList.Items[i]
	}
	return res, nil
}
