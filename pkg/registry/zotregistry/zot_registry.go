package zotregistry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
	"stackdome.io/cluster-agent/pkg/registry"
)

const (
	HtpasswordKey = "htpasswd"
	ZotConfigKey  = "config.json"
)

type zotRegistry struct {
	gcDelay                       string
	gcInterval                    string
	enableGC                      bool
	registryLogLevel              string
	layerCachingOpts              LayerCachingOpts
	RegistryImage                 string
	RegistryConfigReconcilerImage string
	opts                          registry.RegistryBuilderOpts
}

type ZotRegistryOpts struct {
	RegistryImage                 string
	RegistryConfigReconcilerImage string
	GCDelay                       string
	GCInterval                    string
	EnableGC                      bool
	RegistryLogLevel              string
	LayerCachingOpts              LayerCachingOpts
}

type LayerCachingOpts struct {
	Enabled          bool
	RepoGlobPatterns []string
}

func NewZotRegistry(opts ZotRegistryOpts) registry.RegistryBuilder {
	return &zotRegistry{
		gcDelay:                       opts.GCDelay,
		gcInterval:                    opts.GCInterval,
		enableGC:                      opts.EnableGC,
		registryLogLevel:              opts.RegistryLogLevel,
		layerCachingOpts:              opts.LayerCachingOpts,
		RegistryImage:                 opts.RegistryImage,
		RegistryConfigReconcilerImage: opts.RegistryConfigReconcilerImage,
	}
}

func (z *zotRegistry) Initialize(opts registry.RegistryBuilderOpts) error {
	if opts.StorageDirectory == "" {
		return fmt.Errorf("storage directory not provided")
	}
	if opts.ConfigPath == "" {
		return fmt.Errorf("config path not provided")
	}
	if opts.Namespace == "" {
		return fmt.Errorf("registry resources namespace not provided")
	}
	if opts.Client == nil {
		return fmt.Errorf("client not provided")
	}
	z.opts = opts
	return nil
}

func (z *zotRegistry) ValidateConfiguration(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) error {
	if registry.Spec.Port < 1 || registry.Spec.Port > 65535 {
		return fmt.Errorf("port %d is out of valid range (1-65535)", registry.Spec.Port)
	}
	if z.gcDelay != "" {
		if _, err := time.ParseDuration(z.gcDelay); err != nil {
			return fmt.Errorf("invalid gcDelay '%s': %w", z.gcDelay, err)
		}
	}
	if z.gcInterval != "" {
		if _, err := time.ParseDuration(z.gcInterval); err != nil {
			return fmt.Errorf("invalid gcInterval '%s': %w", z.gcInterval, err)
		}
	}
	return nil
}

func (z *zotRegistry) BuildConfigurationConfigMap(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (*corev1.ConfigMap, error) {
	// Build the configuration file for the zot registry.
	config := ZotConfig{
		HTTP: HTTPConfig{
			// Support docker v2 schema 2: application/vnd.docker.distribution.manifest.v2+json
			// More info in: https://github.com/project-zot/zot/issues/724
			Compat:  []string{"docker2s2"},
			Address: "0.0.0.0",
			Port:    fmt.Sprintf("%d", registry.Spec.Port),
		},
		Storage: StorageConfig{
			RootDirectory: z.opts.StorageDirectory,
			GC:            z.enableGC,
			GCDelay:       z.gcDelay,
			GCInterval:    z.gcInterval,
		},
		Log: LogConfig{
			Level: z.registryLogLevel,
		},
	}

	if registry.Spec.Auth != nil && registry.Spec.Auth.HtPasswordCredentials != nil {
		config.HTTP.Auth = AuthConfig{
			Htpasswd: HtpasswdConfig{
				Path: z.opts.Auth.Htpasswd.Path,
			},
		}
	}

	if registry.Spec.RetentionPolicy != nil {
		config.Storage.Retention = z.buildRetentionConfig(registry)
	}

	jsonBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Zot config: %w", err)
	}

	configCM := &corev1.ConfigMap{
		ObjectMeta: v1.ObjectMeta{
			Name:      registry.RegistryConfigMapName(),
			Namespace: z.opts.Namespace,
		},
		Data: map[string]string{
			ZotConfigKey: string(jsonBytes),
		},
	}
	configCM.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ConfigMap"))

	return configCM, nil
}

func (z *zotRegistry) BuildDeployment(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (*appsv1.Deployment, error) {
	configCM := &corev1.ConfigMap{}
	if err := z.opts.Client.Get(
		ctx, client.ObjectKey{Name: registry.RegistryConfigMapName(), Namespace: z.opts.Namespace}, configCM); err != nil {
		return nil, fmt.Errorf("failed to get registry config configmap %s: %w", registry.RegistryConfigMapName(), err)
	}

	config := configCM.Data[ZotConfigKey]
	if config == "" {
		return nil, fmt.Errorf("config map %s is missing zot config %s", registry.RegistryConfigMapName(), ZotConfigKey)
	}

	configHash := fmt.Sprintf("%x", sha256.Sum256([]byte(config)))

	deployment := &appsv1.Deployment{
		ObjectMeta: v1.ObjectMeta{
			Name:      registry.Name,
			Namespace: z.opts.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &v1.LabelSelector{
				MatchLabels: map[string]string{
					"app": registry.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: map[string]string{
						"app": registry.Name,
					},
					// Allow the deployment to be updated if the config changes.
					Annotations: map[string]string{
						"ZotConfigHash": configHash,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  registry.Name,
							Image: z.RegistryImage,
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: registry.Spec.Port,
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "config",
									MountPath: z.opts.ConfigPath,
									SubPath:   ZotConfigKey,
								},
								{
									Name:      "storage",
									MountPath: z.opts.StorageDirectory,
								},
							},
							Args: []string{"serve", z.opts.ConfigPath},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: registry.RegistryConfigMapName(),
									},
								},
							},
						},
						{
							Name: "storage",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: registry.RegistryPVCName(),
								},
							},
						},
					},
				},
			},
		},
	}
	deployment.SetGroupVersionKind(appsv1.SchemeGroupVersion.WithKind("Deployment"))
	if registry.Spec.Auth != nil && registry.Spec.Auth.HtPasswordCredentials != nil {
		htpasswdSecret := &corev1.Secret{}
		if err := z.opts.Client.Get(
			ctx, client.ObjectKey{Name: registry.RegistryAuthSecretName(), Namespace: z.opts.Namespace}, htpasswdSecret,
		); err != nil {
			return nil, fmt.Errorf("failed to get registry auth secret %s: %w", registry.RegistryAuthSecretName(), err)
		}
		// Allow new deployment rollout when htpasswd secret changes.
		htpasswdSecretVersion := htpasswdSecret.ResourceVersion
		deployment.Spec.Template.ObjectMeta.Annotations["HtpasswdSecretVersion"] = htpasswdSecretVersion

		// Mount the htpasswd secret.
		deployment.Spec.Template.Spec.Containers[0].VolumeMounts = append(deployment.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{
			Name:      "htpasswd",
			MountPath: z.opts.Auth.Htpasswd.Path,
			SubPath:   HtpasswordKey,
		})
		deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "htpasswd",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: registry.RegistryAuthSecretName(),
				},
			},
		})

	}

	// Set resource requirements.
	if z.opts.Resources != nil {
		deployment.Spec.Template.Spec.Containers[0].Resources = *z.opts.Resources
	}
	return deployment, nil
}

func (z *zotRegistry) BuildService(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (*corev1.Service, string, error) {
	service := &corev1.Service{
		ObjectMeta: v1.ObjectMeta{
			Name:      registry.Name,
			Namespace: z.opts.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app": registry.Name,
			},
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       int32(80),
					TargetPort: intstr.FromInt32(registry.Spec.Port),
				},
				// TODO: Add support for HTTPS
			},
		},
	}
	service.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Service"))

	registryURL := fmt.Sprintf("http://%s.%s.svc.cluster.local", registry.Name, z.opts.Namespace)
	return service, registryURL, nil
}

func (z *zotRegistry) BuildRegistryConfigReconcilerDaemonset(ctx context.Context, registry *registryv1alpha1.ClusterRegistry, registryConfigCMName string, registryConfigKey string) *appsv1.DaemonSet {
	desiredDaemonset := appsv1.DaemonSet{
		ObjectMeta: v1.ObjectMeta{
			Name:      "registry-config-reconciler",
			Namespace: z.opts.Namespace,
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &v1.LabelSelector{
				MatchLabels: map[string]string{
					"demonset-for": "registry-config-reconciler",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: map[string]string{
						"demonset-for": "registry-config-reconciler",
					},
				},
				Spec: corev1.PodSpec{
					HostPID: true,
					Volumes: []corev1.Volume{
						{
							Name: "registry-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: registryConfigCMName,
									},
									Items: []corev1.KeyToPath{
										{
											Key:  registryConfigKey,
											Path: "registries.json",
										},
									},
								},
							},
						},
						{
							Name: "containerd-config",
							VolumeSource: corev1.VolumeSource{
								HostPath: &corev1.HostPathVolumeSource{
									Path: "/etc/containerd",
									Type: ptr.To(corev1.HostPathDirectory),
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "containerd-registry-config-reconciler",
							Image: z.RegistryConfigReconcilerImage,
							Args: []string{
								"--config-dir=/etc/containerd",
								"--config-file=config.toml",
								"--registry-config=/config/registries.json",
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "containerd-config",
									MountPath: "/etc/containerd",
								},
								{
									Name:      "registry-config",
									MountPath: "/config",
								},
							},
						},
					},
				},
			},
		},
	}

	return &desiredDaemonset
}

func (z *zotRegistry) BuildHTPasswordSecret(ctx context.Context, registry *registryv1alpha1.ClusterRegistry, username, password string) (*corev1.Secret, string, error) {
	if registry.Spec.Auth.HtPasswordCredentials == nil {
		return nil, "", fmt.Errorf("htpasswd credentials not provided")
	}

	htpasswdHash, err := bcrypt.GenerateFromPassword([]byte(password), 5)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate bcrypt hash for htpasswd: %w", err)
	}
	// Arrange in the format of "username:hashed_password"
	usernamePassword := fmt.Sprintf("%s:%s", username, string(htpasswdHash))

	secret := &corev1.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name:      registry.RegistryAuthSecretName(),
			Namespace: z.opts.Namespace,
		},
		Data: map[string][]byte{
			HtpasswordKey: []byte(usernamePassword),
		},
	}
	secret.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	return secret, HtpasswordKey, nil
}

func (z *zotRegistry) buildRetentionConfig(registry *registryv1alpha1.ClusterRegistry) RetentionConfig {
	retention := RetentionConfig{
		Policies: []RetentionPolicy{
			{
				Repositories:   []string{"**"},
				DeleteUntagged: registry.Spec.RetentionPolicy.DeleteUntagged,
				KeepTags: []KeepTagRule{
					{
						MostRecentlyPushedCount: registry.Spec.RetentionPolicy.TagsPerRepo,
					},
				},
			},
		},
	}

	if z.layerCachingOpts.Enabled {
		// Add policy for cache artifacts. We dont want to delete untagged layers for cache artifacts.
		retention.Policies = append(retention.Policies, RetentionPolicy{
			Repositories:   z.layerCachingOpts.RepoGlobPatterns,
			DeleteUntagged: false,
		})
	}

	return retention
}
