package workspaceresource

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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
	"soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

type workloadReconciler struct {
	client.Client
	Scheme                      *runtime.Scheme
	workspaceResourceReconciler *WorkspaceResourceReconciler
}

func GetDeploymentNameForResource(resource *v1alpha1.WorkspaceResource) string {
	return resource.Name
}

func GetDeploymentPodLabelForResource(resource *v1alpha1.WorkspaceResource) map[string]string {
	return map[string]string{
		"resource": GetDeploymentNameForResource(resource),
	}
}

func (r *workloadReconciler) getApplicationBuild(ctx context.Context, resource *v1alpha1.WorkspaceResource) (*v1alpha1.WorkspaceApplicationBuild, error) {
	existingApplicationBuild := &v1alpha1.WorkspaceApplicationBuild{}
	if err := r.Client.Get(ctx,
		types.NamespacedName{
			Name:      ApplicationBuildName(resource),
			Namespace: resource.Namespace,
		},
		existingApplicationBuild,
	); err != nil {
		return nil, err
	}
	return existingApplicationBuild, nil
}

func (r *workloadReconciler) Image(ctx context.Context, resource *v1alpha1.WorkspaceResource) (*string, error) {
	if resource.Spec.ApplicationBuildSpec != nil {
		requiredBuild, err := r.getApplicationBuild(ctx, resource)
		if err != nil {
			return nil, err
		}
		return ptr.To(requiredBuild.Status.ImageUrl), nil
	}
	return ptr.To(resource.Spec.PrebuiltApplicationSpec.Image), nil
}

func (r *workloadReconciler) reconcile(ctx context.Context, resource *v1alpha1.WorkspaceResource) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("in workload reconciler for")
	logger.Info(resource.Name)
	dependencies, err := r.workspaceResourceReconciler.getDependencies(ctx, resource)
	if err != nil {
		return resultNil, err
	}
	logger.Info(fmt.Sprintf("dependencies from workload reconciler: %+v", dependencies))
	volumeInfo, err := r.getVolumeInfo(ctx, resource)
	if err != nil {
		return resultNil, err
	}
	dependencyMapIndex, err := r.makeDependencyMap(ctx, dependencies)
	if err != nil {
		return resultNil, err
	}

	logger.Info(fmt.Sprintf("dependenciesmap from workload reconciler: %+v", dependencyMapIndex))

	image, err := r.Image(ctx, resource)
	if err != nil {
		return resultNil, err
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
			Template: corev1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: GetDeploymentPodLabelForResource(resource),
				},
				Spec: corev1.PodSpec{
					Volumes: InterpolatedVolumesList(resource, volumeInfo),
					Containers: []corev1.Container{
						{
							ImagePullPolicy: corev1.PullIfNotPresent,
							Name:            resource.Name,
							Image:           *image,
							Command:         resource.Spec.Command,
							Args:            resource.Spec.Args,
							Ports:           InterpolatedContainerPorts(resource),
							Env:             InterpolatedEnvVars(resource, dependencyMapIndex),
							VolumeMounts:    InterpolatedVolumeMountList(resource),
						},
					},
				},
			},
		},
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
	if !equality.Semantic.DeepDerivative(desiredDeploymentForResource.Spec, existingDeployment.Spec) {
		logger.Info("Updating existing deployment for workload")
		existingDeployment.Spec = desiredDeploymentForResource.Spec
		return resultRequeue, r.Client.Update(ctx, existingDeployment)
	}
	if controller.DeploymentAvailable(existingDeployment) {
		return resultNil, nil
	}
	logger.Info("deployment not ready")
	reportWorkspaceResourceNotReady(resource, "WorkspaceResouceDeploymentNotReady", "WorkspaceResouceDeploymentNotReady")
	return resultStop, nil
}

func InterpolatedEnvVars(resource *v1alpha1.WorkspaceResource, infoMap map[string]*v1alpha1.WorkspaceResource) []corev1.EnvVar {
	res := make([]corev1.EnvVar, 0)
	for _, envVar := range resource.Spec.EnvironmentVariables {
		if strings.HasPrefix(envVar.Value, "$") {
			referencedResouceName, _ := splitEnvVarValue(envVar.Value)
			// TODO: Assert attribute is Address
			address := infoMap[referencedResouceName].Status.InternalAddress
			res = append(res, corev1.EnvVar{
				Name:  envVar.Name,
				Value: *address,
			})
		} else {
			res = append(res, corev1.EnvVar{
				Name:  envVar.Name,
				Value: envVar.Value,
			})
		}

	}
	return res
}

func InterpolatedContainerPorts(resource *v1alpha1.WorkspaceResource) []corev1.ContainerPort {
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

func InterpolatedVolumeMountList(resource *v1alpha1.WorkspaceResource) []corev1.VolumeMount {
	res := make([]corev1.VolumeMount, 0)
	for _, mount := range resource.Spec.VolumeMounts {
		sourceParts := filepath.SplitList(mount.Source)
		sourceVolumeName := sourceParts[0]
		subPath := filepath.Join(sourceParts[1:]...)
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

func InterpolatedVolumesList(resource *v1alpha1.WorkspaceResource, volumeInfo map[string]*v1alpha1.WorkspaceVolume) []corev1.Volume {
	res := make([]corev1.Volume, 0)
	for _, mount := range resource.Spec.VolumeMounts {
		sourceVolumeName := filepath.SplitList(mount.Source)[0]
		res = append(res, corev1.Volume{
			Name: sourceVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: volumeInfo[sourceVolumeName].Status.PvcName,
				},
			},
		})
	}
	return res
}

func (r *workloadReconciler) getVolumeInfo(ctx context.Context, resource *v1alpha1.WorkspaceResource) (map[string]*v1alpha1.WorkspaceVolume, error) {
	res := make(map[string]*v1alpha1.WorkspaceVolume)
	for _, mount := range resource.Spec.VolumeMounts {
		sourceVolumeName := filepath.SplitList(mount.Source)[0]
		referencedVolume := &v1alpha1.WorkspaceVolume{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: sourceVolumeName, Namespace: resource.Namespace}, referencedVolume); err != nil {
			return nil, fmt.Errorf("failed to get the referenced volume '%s' in resource '%s': %w", sourceVolumeName, resource.Name, err)
		}
		res[sourceVolumeName] = referencedVolume
	}
	return res, nil
}

func (r *workloadReconciler) makeDependencyMap(ctx context.Context, deps []v1alpha1.WorkspaceResource) (map[string]*v1alpha1.WorkspaceResource, error) {
	res := make(map[string]*v1alpha1.WorkspaceResource, len(deps))
	for _, resource := range deps {
		depResource := &v1alpha1.WorkspaceResource{}
		if err := r.Client.Get(ctx, controller.GetNamespacedName(&resource), depResource); err != nil {
			return nil, err
		}
		res[depResource.Name] = depResource
	}
	return res, nil
}

func workspaceResourceName(workspaceName, resourceName string) string {
	return fmt.Sprintf("%s-%s", workspaceName, resourceName)
}
