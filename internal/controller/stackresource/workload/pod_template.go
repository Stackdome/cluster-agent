package workload

import (
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
)

type podTemplateConfig struct {
	resource        *v1alpha1.StackResource
	image           string
	envVars         []corev1.EnvVar
	probes          probeSet
	volumeMountInfo map[string]*storagev1alpha1.Volume
	restartPolicy   corev1.RestartPolicy
	needsRestart    bool
}

func (r *Reconciler) buildPodTemplateSpec(cfg podTemplateConfig) corev1.PodTemplateSpec {
	resource := cfg.resource
	tmpl := corev1.PodTemplateSpec{}
	tmpl.Labels = mergeLabels(GetWorkloadLabelForResource(resource), IdentityLabels(resource))

	c := corev1.Container{
		Name:                     resource.Name,
		Image:                    cfg.image,
		ImagePullPolicy:          resolveImagePullPolicy(resource, cfg.image),
		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
		Command:                  nilIfEmpty(resource.Spec.Command),
		Args:                     nilIfEmpty(resource.Spec.Args),
		Ports:                    nilIfEmpty(containerPorts(resource)),
		Env:                      nilIfEmpty(cfg.envVars),
		VolumeMounts:             nilIfEmpty(volumeMountList(resource)),
		ReadinessProbe:           cfg.probes.readiness,
		LivenessProbe:            cfg.probes.liveness,
		StartupProbe:             cfg.probes.startup,
	}
	if resource.Spec.Resources != nil {
		c.Resources = *resource.Spec.Resources
	}
	tmpl.Spec.Containers = []corev1.Container{c}
	if cfg.restartPolicy != "" {
		tmpl.Spec.RestartPolicy = cfg.restartPolicy
	}
	tmpl.Spec.Volumes = nilIfEmpty(volumesList(resource, cfg.volumeMountInfo))
	if resource.Spec.TerminationGracePeriodSeconds != nil {
		tmpl.Spec.TerminationGracePeriodSeconds = resource.Spec.TerminationGracePeriodSeconds
	}
	if resource.Spec.Init != nil {
		initImage := cfg.image
		if resource.Spec.Init.ImageSpec != nil {
			initImage = resource.Spec.Init.ImageSpec.Image
		}
		tmpl.Spec.InitContainers = []corev1.Container{{
			Name:                     resource.InitContainerName(),
			Image:                    initImage,
			ImagePullPolicy:          resolveImagePullPolicy(resource, initImage),
			TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
			Command:                  nilIfEmpty(resource.Spec.Init.Command),
			Args:                     nilIfEmpty(resource.Spec.Init.Args),
			Env:                      nilIfEmpty(cfg.envVars),
			VolumeMounts:             nilIfEmpty(volumeMountList(resource)),
		}}
	}
	applySecurityDefaults(&tmpl.Spec, resource.Spec.HardenedSecurityDefaults)
	if cfg.needsRestart {
		if tmpl.Annotations == nil {
			tmpl.Annotations = map[string]string{}
		}
		tmpl.Annotations[v1alpha1.RestartResourceAnnotation] = v1.Now().UTC().String()
	}
	return tmpl
}
