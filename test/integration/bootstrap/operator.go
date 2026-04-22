package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/go-logr/logr"
	"github.com/mt-sre/devkube/dev"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	operatorImage     = "stackdome/cluster-agent-manager:integration-test"
	operatorNamespace = "stackdome-control-plane"
)

type OperatorManager struct {
	cluster *dev.Cluster
	logger  logr.Logger
}

func NewOperatorManager(cluster *dev.Cluster, logger logr.Logger) *OperatorManager {
	return &OperatorManager{
		cluster: cluster,
		logger:  logger,
	}
}

func (om *OperatorManager) Deploy(ctx context.Context) error {
	image := os.Getenv("OPERATOR_IMAGE")
	if image == "" {
		om.logger.Info("Building operator image locally")
		if err := om.buildAndLoadImage(ctx); err != nil {
			return fmt.Errorf("building operator image: %w", err)
		}
		image = operatorImage
	} else {
		om.logger.Info("Using pre-built operator image", "image", image)
	}

	om.logger.Info("Deploying operator namespace and RBAC")
	if err := om.cluster.CreateAndWaitFromFiles(ctx, []string{
		"config/deploy/00-namespace.yaml",
		"config/deploy/01-rbac.yaml",
	}); err != nil {
		return fmt.Errorf("deploying namespace/RBAC: %w", err)
	}

	om.logger.Info("Deploying operator")
	deployment := buildOperatorDeployment(image)
	if err := om.cluster.CreateAndWaitForReadiness(ctx, deployment); err != nil {
		return fmt.Errorf("deploying operator: %w", err)
	}

	om.logger.Info("Operator deployed and ready")
	return nil
}

func (om *OperatorManager) buildAndLoadImage(ctx context.Context) error {
	projectRoot, err := findProjectRoot()
	if err != nil {
		return err
	}

	// Build the binary for linux/amd64
	om.logger.Info("Compiling operator binary")
	buildCmd := exec.CommandContext(ctx, "go", "build", "-v", "-o",
		filepath.Join(projectRoot, "bin/linux_amd64/cluster-agent-manager"),
		"./cmd/cluster-agent/cluster-agent-manager.go")
	buildCmd.Dir = projectRoot
	buildCmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if output, err := buildCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go build failed: %w\n%s", err, string(output))
	}

	// Build Docker image
	om.logger.Info("Building Docker image")
	dockerfileContent := `FROM alpine:3.21.3
RUN apk add --no-cache ca-certificates
WORKDIR /
COPY cluster-agent-manager /usr/local/bin/
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/cluster-agent-manager"]
`
	buildCtxDir := filepath.Join(projectRoot, ".cache/integration-test/image-build")
	if err := os.MkdirAll(buildCtxDir, 0755); err != nil {
		return fmt.Errorf("creating build context dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(buildCtxDir, "Dockerfile"), []byte(dockerfileContent), 0644); err != nil {
		return fmt.Errorf("writing Dockerfile: %w", err)
	}
	cpCmd := exec.CommandContext(ctx, "cp",
		filepath.Join(projectRoot, "bin/linux_amd64/cluster-agent-manager"),
		filepath.Join(buildCtxDir, "cluster-agent-manager"))
	if output, err := cpCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copying binary: %w\n%s", err, string(output))
	}

	dockerCmd := exec.CommandContext(ctx, "docker", "build", "-t", operatorImage, buildCtxDir)
	if output, err := dockerCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker build failed: %w\n%s", err, string(output))
	}

	// Load into Kind
	om.logger.Info("Loading image into Kind cluster")
	kindCmd := exec.CommandContext(ctx, "kind", "load", "docker-image", operatorImage, "--name", clusterName)
	if output, err := kindCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("kind load failed: %w\n%s", err, string(output))
	}

	return nil
}

func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find project root (no go.mod found)")
		}
		dir = parent
	}
}

func buildOperatorDeployment(image string) *appsv1.Deployment {
	replicas := int32(1)
	runAsNonRoot := true
	allowPrivilegeEscalation := false
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "stackdome-operator-manager",
			Namespace: operatorNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/name": "stackdome-operator",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/name": "stackdome-operator",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/name": "stackdome-operator",
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "stackdome-operator",
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &runAsNonRoot,
						SeccompProfile: &corev1.SeccompProfile{
							Type: corev1.SeccompProfileTypeRuntimeDefault,
						},
					},
					Containers: []corev1.Container{
						{
							Name:            "manager",
							Image:           image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Args:            []string{"--leader-elect"},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromInt(8081),
									},
								},
								InitialDelaySeconds: 15,
								PeriodSeconds:       20,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/readyz",
										Port: intstr.FromInt(8081),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       10,
							},
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
							},
						},
					},
				},
			},
		},
	}
}
