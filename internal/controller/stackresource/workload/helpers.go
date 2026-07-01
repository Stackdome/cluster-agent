package workload

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
)

func buildEnvVars(resource *v1alpha1.StackResource) []corev1.EnvVar {
	res := make([]corev1.EnvVar, 0, len(resource.Spec.EnvironmentVariables))
	for _, env := range resource.Spec.EnvironmentVariables {
		if env.ValueFrom != nil {
			res = append(res, corev1.EnvVar{
				Name: env.Name,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &env.ValueFrom.SecretKeyRef,
				},
			})
		} else {
			res = append(res, corev1.EnvVar{
				Name:  env.Name,
				Value: env.Value,
			})
		}
	}
	return res
}

type probeSet struct {
	readiness *corev1.Probe
	liveness  *corev1.Probe
	startup   *corev1.Probe
}

func buildProbes(resource *v1alpha1.StackResource) (probeSet, error) {
	hc := resource.Spec.HealthChecks
	if hc == nil {
		return probeSet{}, nil
	}
	readiness, err := buildProbe(hc.Readiness, resource.Spec.Ports)
	if err != nil {
		return probeSet{}, fmt.Errorf("readiness probe: %w", err)
	}
	liveness, err := buildProbe(hc.Liveness, resource.Spec.Ports)
	if err != nil {
		return probeSet{}, fmt.Errorf("liveness probe: %w", err)
	}
	startup, err := buildProbe(hc.Startup, resource.Spec.Ports)
	if err != nil {
		return probeSet{}, fmt.Errorf("startup probe: %w", err)
	}
	return probeSet{readiness: readiness, liveness: liveness, startup: startup}, nil
}

func buildProbe(p *v1alpha1.Probe, ports []v1alpha1.Port) (*corev1.Probe, error) {
	if p == nil {
		return nil, nil
	}
	probe := &corev1.Probe{
		InitialDelaySeconds: p.InitialDelaySeconds,
		PeriodSeconds:       p.PeriodSeconds,
		FailureThreshold:    p.FailureThreshold,
		TimeoutSeconds:      p.TimeoutSeconds,
	}
	switch {
	case p.HTTPGet != nil:
		number, ok := portNumberByName(p.HTTPGet.PortName, ports)
		if !ok {
			return nil, fmt.Errorf("probe references unknown port %q", p.HTTPGet.PortName)
		}
		probe.ProbeHandler = corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: p.HTTPGet.Path, Port: intstr.FromInt32(number)},
		}
	case p.TCPSocket != nil:
		number, ok := portNumberByName(p.TCPSocket.PortName, ports)
		if !ok {
			return nil, fmt.Errorf("probe references unknown port %q", p.TCPSocket.PortName)
		}
		probe.ProbeHandler = corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(number)},
		}
	case len(p.Command) > 0:
		probe.ProbeHandler = corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: p.Command}}
	}
	return probe, nil
}

func portNumberByName(name string, ports []v1alpha1.Port) (int32, bool) {
	for _, p := range ports {
		if p.Name == name {
			return p.Number, true
		}
	}
	return 0, false
}

func containerPorts(resource *v1alpha1.StackResource) []corev1.ContainerPort {
	res := make([]corev1.ContainerPort, 0, len(resource.Spec.Ports))
	for _, port := range resource.Spec.Ports {
		res = append(res, corev1.ContainerPort{
			Name:          port.Name,
			ContainerPort: port.Number,
		})
	}
	return res
}

func applySecurityDefaults(podSpec *corev1.PodSpec, hardened *bool) {
	if hardened == nil || !*hardened {
		return
	}
	podSpec.SecurityContext = &corev1.PodSecurityContext{
		RunAsNonRoot: ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
	for i := range podSpec.Containers {
		podSpec.Containers[i].SecurityContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}
	}
	for i := range podSpec.InitContainers {
		podSpec.InitContainers[i].SecurityContext = &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		}
	}
}

func volumeMountList(resource *v1alpha1.StackResource) []corev1.VolumeMount {
	if len(resource.Spec.VolumeMounts) == 0 {
		return []corev1.VolumeMount{}
	}
	res := make([]corev1.VolumeMount, 0, len(resource.Spec.VolumeMounts))
	for _, mount := range resource.Spec.VolumeMounts {
		vm := corev1.VolumeMount{
			Name:      mount.SourceVolume,
			MountPath: mount.Destination,
			ReadOnly:  mount.ReadOnly,
		}
		if len(mount.SourceSubPath) > 0 {
			vm.SubPath = strings.TrimPrefix(mount.SourceSubPath, "/")
		}
		res = append(res, vm)
	}
	return res
}

func volumesList(resource *v1alpha1.StackResource, volumeInfo map[string]*storagev1alpha1.Volume) []corev1.Volume {
	if len(resource.Spec.VolumeMounts) == 0 {
		return []corev1.Volume{}
	}
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

func (r *Reconciler) getVolumeMountInfoMap(ctx context.Context, resource *v1alpha1.StackResource) (map[string]*storagev1alpha1.Volume, error) {
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

// resolveImagePullPolicy returns the pull policy for a container image.
// Precedence: explicit ImageSpec policy > tag-based inference.
// Untagged refs and "latest" use PullAlways; all other tags use PullIfNotPresent.
func resolveImagePullPolicy(resource *v1alpha1.StackResource, image string) corev1.PullPolicy {
	if resource.Spec.ImageSpec != nil && resource.Spec.ImageSpec.ImagePullPolicy != "" {
		return resource.Spec.ImageSpec.ImagePullPolicy
	}

	if i := strings.LastIndex(image, ":"); i < 0 || image[i+1:] == "latest" {
		return corev1.PullAlways
	}
	return corev1.PullIfNotPresent
}

func nilIfEmpty[T any](s []T) []T {
	if len(s) == 0 {
		return nil
	}
	return s
}
