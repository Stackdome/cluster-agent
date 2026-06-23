package bootstrap

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
	"stackdome.io/cluster-agent/pkg/config"
	reg "stackdome.io/cluster-agent/pkg/registry"
)

const (
	RegistryName      = "int-test-registry"
	RegistryNamespace = "stackdome-registry"

	registryName       = RegistryName
	registryNamespace  = RegistryNamespace
	registryCredSecret = "registry-creds"
	registryPort       = int32(5000)
	registryUsername   = "admin"
	registryPassword   = "admin"
)

type RegistryManager struct {
	client     client.Client
	kubeClient kubernetes.Interface
	logger     logr.Logger
}

func NewRegistryManager(c client.Client, kubeClient kubernetes.Interface, logger logr.Logger) *RegistryManager {
	return &RegistryManager{client: c, kubeClient: kubeClient, logger: logger}
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
			"username": registryUsername,
			"password": registryPassword,
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
			return fmt.Errorf("timed out waiting for registry to become Running\n%s", rm.dumpRegistryDiagnostics(ctx))
		case <-tick.C:
			cr := &registryv1alpha1.ClusterRegistry{}
			if err := rm.client.Get(ctx, client.ObjectKey{Name: registryName}, cr); err != nil {
				continue
			}
			if cr.Status.Phase == registryv1alpha1.RegistryPhaseRunning {
				rm.logger.Info("Registry is running", "internalURL", cr.Status.InternalURL)
				return nil
			}
		}
	}
}

func (rm *RegistryManager) dumpRegistryDiagnostics(ctx context.Context) string {
	out := "\n=== Registry Bootstrap Diagnostics ===\n"

	cr := &registryv1alpha1.ClusterRegistry{}
	if err := rm.client.Get(ctx, client.ObjectKey{Name: registryName}, cr); err != nil {
		out += fmt.Sprintf("failed to get ClusterRegistry: %v\n", err)
	} else {
		out += fmt.Sprintf("ClusterRegistry: %s\n  Phase: %s\n  InternalURL: %s\n  ServiceIP: %s\n", cr.Name, cr.Status.Phase, cr.Status.InternalURL, cr.Status.ServiceIP)
		for _, cond := range cr.Status.Conditions {
			out += fmt.Sprintf("  Condition %s=%s (reason=%s, message=%s)\n", cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}

	dep := &appsv1.Deployment{}
	if err := rm.client.Get(ctx, client.ObjectKey{Name: registryName, Namespace: registryNamespace}, dep); err != nil {
		out += fmt.Sprintf("\nfailed to get registry Deployment: %v\n", err)
	} else {
		out += fmt.Sprintf("\nDeployment: %s/%s\n  Ready: %d/%d\n", dep.Namespace, dep.Name, dep.Status.ReadyReplicas, dep.Status.Replicas)
	}

	ds := &appsv1.DaemonSet{}
	if err := rm.client.Get(ctx, client.ObjectKey{Name: reg.RegistryConfigReconcilerDaemonSetName, Namespace: registryNamespace}, ds); err != nil {
		out += fmt.Sprintf("\nfailed to get DaemonSet: %v\n", err)
	} else {
		out += fmt.Sprintf("\nDaemonSet: %s/%s\n  Desired=%d Available=%d Ready=%d\n", ds.Namespace, ds.Name, ds.Status.DesiredNumberScheduled, ds.Status.NumberAvailable, ds.Status.NumberReady)
	}

	pods := &corev1.PodList{}
	if err := rm.client.List(ctx, pods, client.InNamespace(registryNamespace)); err != nil {
		out += fmt.Sprintf("\nfailed to list pods: %v\n", err)
	} else {
		for _, p := range pods.Items {
			out += fmt.Sprintf("\nPod: %s (node=%s) phase=%s\n", p.Name, p.Spec.NodeName, p.Status.Phase)
			for _, cs := range p.Status.ContainerStatuses {
				out += fmt.Sprintf("  Container %s: ready=%v restarts=%d\n", cs.Name, cs.Ready, cs.RestartCount)
				if cs.State.Waiting != nil {
					out += fmt.Sprintf("    waiting: reason=%s message=%s\n", cs.State.Waiting.Reason, cs.State.Waiting.Message)
				}
			}
			if rm.kubeClient != nil {
				out += rm.fetchPodLogs(ctx, p.Name, 30)
			}
		}
	}

	out += "=== End Registry Bootstrap Diagnostics ===\n"
	return out
}

func (rm *RegistryManager) fetchPodLogs(ctx context.Context, podName string, tailLines int64) string {
	out := fmt.Sprintf("  Logs (last %d lines):\n", tailLines)
	req := rm.kubeClient.CoreV1().Pods(registryNamespace).GetLogs(podName, &corev1.PodLogOptions{TailLines: &tailLines})
	stream, err := req.Stream(ctx)
	if err != nil {
		return out + fmt.Sprintf("    <failed to fetch logs: %v>\n", err)
	}
	defer stream.Close()
	data, err := io.ReadAll(stream)
	if err != nil {
		return out + fmt.Sprintf("    <failed to read logs: %v>\n", err)
	}
	out += fmt.Sprintf("    %s\n", string(data))
	return out
}
