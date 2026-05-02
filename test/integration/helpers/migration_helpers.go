package helpers

import (
	"context"
	"fmt"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
)

func WaitForDeploymentFieldChange(ctx context.Context, c client.Client, key client.ObjectKey, previousRV string, timeout time.Duration) (*appsv1.Deployment, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			dep := &appsv1.Deployment{}
			_ = c.Get(ctx, key, dep)
			return dep, fmt.Errorf("timed out waiting for Deployment %s ResourceVersion to change from %s (current: %s)", key.Name, previousRV, dep.ResourceVersion)
		case <-tick.C:
			dep := &appsv1.Deployment{}
			if err := c.Get(ctx, key, dep); err != nil {
				continue
			}
			if dep.ResourceVersion != previousRV {
				return dep, nil
			}
		}
	}
}

func VerifyResourceVersionStable(ctx context.Context, c client.Client, key client.ObjectKey, expectedRV string, stabilityWindow time.Duration) error {
	deadline := time.After(stabilityWindow)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			return nil
		case <-tick.C:
			dep := &appsv1.Deployment{}
			if err := c.Get(ctx, key, dep); err != nil {
				return fmt.Errorf("failed to get Deployment %s: %w", key.Name, err)
			}
			if dep.ResourceVersion != expectedRV {
				return fmt.Errorf("Deployment %s ResourceVersion changed from %s to %s (spurious update detected)", key.Name, expectedRV, dep.ResourceVersion)
			}
		}
	}
}

func WaitForCNPGClusterSpecChange(ctx context.Context, c client.Client, key client.ObjectKey, previousRV string, timeout time.Duration) (*cnpgv1.Cluster, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			cluster := &cnpgv1.Cluster{}
			_ = c.Get(ctx, key, cluster)
			return cluster, fmt.Errorf("timed out waiting for CNPG Cluster %s ResourceVersion to change from %s (current: %s)", key.Name, previousRV, cluster.ResourceVersion)
		case <-tick.C:
			cluster := &cnpgv1.Cluster{}
			if err := c.Get(ctx, key, cluster); err != nil {
				continue
			}
			if cluster.ResourceVersion != previousRV {
				return cluster, nil
			}
		}
	}
}

func VerifyCNPGClusterResourceVersionStable(ctx context.Context, c client.Client, key client.ObjectKey, expectedRV string, stabilityWindow time.Duration) error {
	deadline := time.After(stabilityWindow)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			return nil
		case <-tick.C:
			cluster := &cnpgv1.Cluster{}
			if err := c.Get(ctx, key, cluster); err != nil {
				return fmt.Errorf("failed to get CNPG Cluster %s: %w", key.Name, err)
			}
			if cluster.ResourceVersion != expectedRV {
				return fmt.Errorf("CNPG Cluster %s ResourceVersion changed from %s to %s (spurious update detected)", key.Name, expectedRV, cluster.ResourceVersion)
			}
		}
	}
}

func GetRegistryDeployment(ctx context.Context, c client.Client, registryName string) (*appsv1.Deployment, error) {
	dep := &appsv1.Deployment{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      registryName,
		Namespace: "stackdome-registry",
	}, dep)
	return dep, err
}

func GetRegistryConfigMap(ctx context.Context, c client.Client, registryName string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      registryName + "-config",
		Namespace: "stackdome-registry",
	}, cm)
	return cm, err
}

func WaitForRegistryReady(ctx context.Context, c client.Client, name string, timeout time.Duration) (*registryv1alpha1.ClusterRegistry, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			reg := &registryv1alpha1.ClusterRegistry{}
			_ = c.Get(ctx, client.ObjectKey{Name: name}, reg)
			return reg, fmt.Errorf("timed out waiting for ClusterRegistry %s to become Running (current phase: %s)", name, reg.Status.Phase)
		case <-tick.C:
			reg := &registryv1alpha1.ClusterRegistry{}
			if err := c.Get(ctx, client.ObjectKey{Name: name}, reg); err != nil {
				continue
			}
			if reg.Status.Phase == registryv1alpha1.RegistryPhaseRunning {
				return reg, nil
			}
		}
	}
}
