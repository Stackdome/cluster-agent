package bootstrap

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Environment struct {
	Client          client.Client
	KubeClient      kubernetes.Interface
	Scheme          *k8sruntime.Scheme
	TestNamespace   string
	ObjectStoreName string
	RegistryURL     string
	Logger          logr.Logger

	clusterManager *ClusterManager
}

func Setup(env *Environment, ctx context.Context) error {
	logger := stdr.New(log.New(os.Stderr, "[integration] ", log.LstdFlags))
	env.Logger = logger
	env.TestNamespace = TestNamespace
	env.ObjectStoreName = ObjectStoreName

	// Ensure we run from the project root so relative paths
	// (config/deploy/crds, config/deploy/barman-cloud-manifest.yaml, etc.) resolve.
	projectRoot, err := findProjectRoot()
	if err != nil {
		return fmt.Errorf("finding project root: %w", err)
	}
	if err := os.Chdir(projectRoot); err != nil {
		return fmt.Errorf("changing to project root: %w", err)
	}
	logger.Info("Working directory set to project root", "path", projectRoot)

	// Phase 1: Kind cluster + dependencies + S3Mock
	logger.Info("Phase 1: Kind cluster bootstrap")
	cm := NewClusterManager(logger)
	env.clusterManager = cm
	if err := cm.Bootstrap(ctx); err != nil {
		return fmt.Errorf("cluster bootstrap: %w", err)
	}

	// Build client from cluster
	scheme := k8sruntime.NewScheme()
	for _, addToScheme := range schemeBuilder() {
		if err := addToScheme(scheme); err != nil {
			return fmt.Errorf("adding scheme: %w", err)
		}
	}
	env.Scheme = scheme

	restConfig := cm.GetDevCluster().RestConfig
	c, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("creating controller-runtime client: %w", err)
	}
	env.Client = c

	kubeClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %w", err)
	}
	env.KubeClient = kubeClient

	// Deploy S3Mock as cluster-level infrastructure
	sm := NewS3MockManager(c, logger)
	if err := sm.DeployInfra(ctx); err != nil {
		return fmt.Errorf("s3mock infra deployment: %w", err)
	}

	// Pre-load images needed for registry and builds into Kind
	rm := NewRegistryManager(c, kubeClient, logger)
	if err := rm.PreloadImages(ctx); err != nil {
		return fmt.Errorf("preloading images: %w", err)
	}

	// Phase 2: Deploy operator
	logger.Info("Phase 2: Operator deployment")
	om := NewOperatorManager(cm.GetDevCluster(), logger)
	if err := om.Deploy(ctx); err != nil {
		return fmt.Errorf("operator deployment: %w", err)
	}

	// Phase 2b: Set up in-cluster registry
	logger.Info("Phase 2b: Registry setup")
	if err := rm.Setup(ctx); err != nil {
		return fmt.Errorf("registry setup: %w", err)
	}
	env.RegistryURL = fmt.Sprintf("%s.%s.svc.cluster.local", registryName, registryNamespace)

	// Phase 3: Test prerequisites (namespace, ImageCatalog, S3 credentials, ObjectStore)
	logger.Info("Phase 3: Prerequisites")
	pm := NewPrerequisiteManager(c, logger)
	if err := pm.Setup(ctx); err != nil {
		return fmt.Errorf("prerequisite setup: %w", err)
	}
	if err := sm.CreateObjectStore(ctx, TestNamespace); err != nil {
		return fmt.Errorf("objectstore setup: %w", err)
	}

	// Brief pause to let the operator pick up the new CRDs/resources
	time.Sleep(10 * time.Second)

	logger.Info("Integration test bootstrap complete")
	return nil
}

func (env *Environment) Cleanup() {
	if env.Logger.GetSink() == nil {
		env.Logger = stdr.New(log.New(os.Stderr, "[integration] ", log.LstdFlags))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if env.clusterManager != nil {
		if err := env.clusterManager.Cleanup(ctx); err != nil {
			env.Logger.Error(err, "Failed to cleanup cluster")
		}
	}
}
