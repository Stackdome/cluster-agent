package bootstrap

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
	"stackdome.io/cluster-agent/pkg/config"
)

const (
	registryName       = "int-test-registry"
	registryNamespace  = "stackdome-registry"
	registryCredSecret = "registry-creds"
	registryPort       = int32(5000)
)

type RegistryManager struct {
	client client.Client
	logger logr.Logger
}

func NewRegistryManager(c client.Client, logger logr.Logger) *RegistryManager {
	return &RegistryManager{client: c, logger: logger}
}

// Setup creates the registry namespace, credentials secret, and ClusterRegistry CR,
// then waits for the registry to reach the Running phase.
func (rm *RegistryManager) Setup(ctx context.Context) error {
	rm.logger.Info("Setting up registry infrastructure")

	// Create registry namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: registryNamespace},
	}
	if err := rm.client.Create(ctx, ns); err != nil {
		return fmt.Errorf("creating registry namespace: %w", err)
	}

	// Create credentials secret
	credSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      registryCredSecret,
			Namespace: registryNamespace,
		},
		StringData: map[string]string{
			"username": "admin",
			"password": "admin",
		},
	}
	if err := rm.client.Create(ctx, credSecret); err != nil {
		return fmt.Errorf("creating registry credentials secret: %w", err)
	}

	// Create ClusterRegistry CR (cluster-scoped)
	registry := &registryv1alpha1.ClusterRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name: registryName,
		},
		Spec: registryv1alpha1.ClusterRegistrySpec{
			Owner: registryv1alpha1.RegistryOwner{
				Type: "Organization",
				ID:   "integration-test",
			},
			Storage: registryv1alpha1.RegistryStorageSpec{
				Size: "5Gi",
			},
			Auth: &registryv1alpha1.RegistryAuthSpec{
				HtPasswordCredentials: &registryv1alpha1.HtPasswordCredentialsSpec{
					CredentialsRef: &corev1alpha1.CredentialSecretKeyPair{
						SecretRef: corev1.SecretReference{
							Name:      registryCredSecret,
							Namespace: registryNamespace,
						},
						UsernameKey: "username",
						PasswordKey: "password",
					},
				},
			},
			Port: registryPort,
		},
	}
	if err := rm.client.Create(ctx, registry); err != nil {
		return fmt.Errorf("creating ClusterRegistry: %w", err)
	}

	// Wait for registry to become Running
	rm.logger.Info("Waiting for registry to become Running")
	if err := rm.waitForRegistryReady(ctx); err != nil {
		return fmt.Errorf("waiting for registry: %w", err)
	}

	rm.logger.Info("Registry infrastructure ready")
	return nil
}

// PreloadImages pulls required images and loads them into the Kind cluster.
func (rm *RegistryManager) PreloadImages(ctx context.Context) error {
	images := []string{
		config.KanikoExecutorImage,
		config.ZotImage,
		config.RegistryConfigReconcilerImage,
	}
	for _, img := range images {
		rm.logger.Info("Pulling image", "image", img)
		pullCmd := exec.CommandContext(ctx, "docker", "pull", img)
		if output, err := pullCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("docker pull %s: %w\n%s", img, err, string(output))
		}

		rm.logger.Info("Loading image into Kind", "image", img)
		loadCmd := exec.CommandContext(ctx, "kind", "load", "docker-image", img, "--name", clusterName)
		if output, err := loadCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("kind load %s: %w\n%s", img, err, string(output))
		}
	}
	return nil
}

func (rm *RegistryManager) waitForRegistryReady(ctx context.Context) error {
	timeout := time.After(5 * time.Minute)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timed out waiting for registry to become Running")
		case <-tick.C:
			reg := &registryv1alpha1.ClusterRegistry{}
			if err := rm.client.Get(ctx, client.ObjectKey{Name: registryName}, reg); err != nil {
				continue
			}
			if reg.Status.Phase == registryv1alpha1.RegistryPhaseRunning {
				rm.logger.Info("Registry is running", "internalURL", reg.Status.InternalURL)
				return nil
			}
		}
	}
}
