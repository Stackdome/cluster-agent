package registry

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
)

const (
	RegistryConfigReconcilerDaemonSetName = "registry-config-reconciler"
)

type RuntimeType string

const (
	RuntimeContainerd RuntimeType = "containerd"
	RuntimeK3s        RuntimeType = "k3s"
)

type RegistryBuilder interface {
	Initialize(opts RegistryBuilderOpts) error
	BuildConfigurationConfigMap(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (*corev1.ConfigMap, error)
	BuildDeployment(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (*appsv1.Deployment, error)
	BuildService(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) (*corev1.Service, string, error)
	BuildRegistryConfigReconcilerDaemonset(ctx context.Context, registry *registryv1alpha1.ClusterRegistry, registryConfigCMName string, registryConfigKey string, runtime RuntimeType) *appsv1.DaemonSet
	BuildHTPasswordSecret(ctx context.Context, registry *registryv1alpha1.ClusterRegistry, username, password string) (*corev1.Secret, string, error)
	ValidateConfiguration(ctx context.Context, registry *registryv1alpha1.ClusterRegistry) error
}

type AuthOpts struct {
	Htpasswd HtpasswdOpts
}

type HtpasswdOpts struct {
	Path string
}

type RegistryBuilderOpts struct {
	Client           client.Client                // Client for fetching resources
	StorageDirectory string                       // Base storage directory
	ConfigPath       string                       // Path to mount config in container
	Auth             AuthOpts                     // Authentication options
	Resources        *corev1.ResourceRequirements // Resource limits/requests
	Namespace        string                       // Namespace for registry resources
}
