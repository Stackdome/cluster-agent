package bootstrap

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	"github.com/mt-sre/devkube/dev"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	kindv1alpha4 "sigs.k8s.io/kind/pkg/apis/config/v1alpha4"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	barmancloudv1 "github.com/cloudnative-pg/plugin-barman-cloud/api/v1"
	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	usersv1alpha1 "stackdome.io/cluster-agent/api/users/v1alpha1"
)

const (
	clusterName = "stackdome-int-test"
	cacheDir    = ".cache/integration-test"
)

type ClusterManager struct {
	environment    *dev.Environment
	logger         logr.Logger
	createdCluster bool
}

func NewClusterManager(logger logr.Logger) *ClusterManager {
	if logger.GetSink() == nil {
		logger = stdr.New(log.New(os.Stderr, "", log.LstdFlags))
	}
	return &ClusterManager{logger: logger}
}

func (cm *ClusterManager) Bootstrap(ctx context.Context) error {
	cm.logger.Info("Starting Kind cluster bootstrap")
	ctrl.SetLogger(cm.logger)

	// Delete any stale Kind cluster with the same name to ensure a clean start.
	// devkube reuses existing clusters by name, but the cached kubeconfig may be
	// invalid if the cluster was created by another environment.
	if err := deleteExistingCluster(clusterName, cm.logger); err != nil {
		cm.logger.Info("No existing cluster to clean up", "reason", err.Error())
	}

	clusterInitializers := dev.WithClusterInitializers{
		dev.ClusterLoadObjectsFromFolders{
			"config/deploy/crds",
		},
		dev.ClusterHelmInstall{
			RepoName:    "cnpg",
			RepoURL:     "https://cloudnative-pg.github.io/charts",
			PackageName: "cloudnative-pg",
			Namespace:   "cnpg-system",
			ReleaseName: "cnpg",
		},
		dev.ClusterHelmInstall{
			RepoName:    "jetstack",
			RepoURL:     "https://charts.jetstack.io",
			PackageName: "cert-manager",
			Namespace:   "cert-manager",
			ReleaseName: "cert-manager",
			SetVars: []string{
				"crds.enabled=true",
				"installCRDs=true",
			},
		},
		dev.ClusterLoadObjectsFromFiles{
			"config/deploy/barman-cloud-manifest.yaml",
		},
	}

	cm.environment = dev.NewEnvironment(
		clusterName,
		path.Join(cacheDir, clusterName),
		dev.WithClusterOptions([]dev.ClusterOption{
			dev.WithWaitOptions([]dev.WaitOption{
				dev.WithTimeout(10 * time.Minute),
			}),
			dev.WithSchemeBuilder(schemeBuilder()),
		}),
		dev.WithContainerRuntime("docker"),
		dev.WithKindClusterConfig(kindv1alpha4.Cluster{
			Nodes: []kindv1alpha4.Node{
				{Role: kindv1alpha4.ControlPlaneRole},
				{Role: kindv1alpha4.WorkerRole},
				{Role: kindv1alpha4.WorkerRole},
			},
		}),
		clusterInitializers,
	)

	bootstrapCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if err := cm.environment.Init(bootstrapCtx); err != nil {
		return fmt.Errorf("initializing Kind cluster: %w", err)
	}
	cm.createdCluster = true
	cm.logger.Info("Kind cluster bootstrap complete")
	return nil
}

func (cm *ClusterManager) GetDevCluster() *dev.Cluster {
	if cm.environment == nil {
		return nil
	}
	return cm.environment.Cluster
}

func (cm *ClusterManager) GetEnvironment() *dev.Environment {
	return cm.environment
}

func (cm *ClusterManager) Cleanup(ctx context.Context) error {
	if !cm.createdCluster || cm.environment == nil {
		return nil
	}
	if os.Getenv("KEEP_CLUSTER") == "true" {
		cm.logger.Info("KEEP_CLUSTER=true, preserving cluster", "name", clusterName)
		return nil
	}
	cm.logger.Info("Tearing down Kind cluster")
	return cm.environment.Destroy(ctx)
}

func deleteExistingCluster(name string, logger logr.Logger) error {
	out, err := exec.Command("kind", "get", "clusters").CombinedOutput()
	if err != nil {
		return fmt.Errorf("listing kind clusters: %w", err)
	}
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("cluster %s not found", name)
	}
	logger.Info("Deleting existing Kind cluster", "name", name)
	if out, err := exec.Command("kind", "delete", "cluster", "--name", name).CombinedOutput(); err != nil {
		return fmt.Errorf("deleting cluster: %s: %w", string(out), err)
	}
	// Clean stale cache so devkube doesn't pick up an empty kubeconfig
	clusterCacheDir := path.Join(cacheDir, name)
	_ = os.RemoveAll(clusterCacheDir)
	logger.Info("Deleted existing Kind cluster and cleaned cache", "name", name)
	return nil
}

func schemeBuilder() k8sruntime.SchemeBuilder {
	return k8sruntime.SchemeBuilder{
		clientgoscheme.AddToScheme,
		addonsv1alpha1.AddToScheme,
		buildsv1alpha1.AddToScheme,
		corev1alpha1.AddToScheme,
		registryv1alpha1.AddToScheme,
		storagev1alpha1.AddToScheme,
		usersv1alpha1.AddToScheme,
		cnpgv1.AddToScheme,
		barmancloudv1.AddToScheme,
	}
}
