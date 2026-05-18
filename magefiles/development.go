//go:build mage
// +build mage

package main

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/magefile/mage/mg"
	"github.com/mt-sre/devkube/dev"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	kindv1alpha4 "sigs.k8s.io/kind/pkg/apis/config/v1alpha4"
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	usersv1alpha1 "stackdome.io/cluster-agent/api/users/v1alpha1"
)

type Dev mg.Namespace

var (
	devEnvironment *dev.Environment
)

func (d Dev) init() error {
	mg.SerialDeps(
		setupContainerRuntime,
		Dependency.Kind,
		Dependency.CloudProviderKind,
	)

	ctrl.SetLogger(logger)

	clusterInitializers := dev.WithClusterInitializers{
		dev.ClusterLoadObjectsFromFolders{
			"config/deploy/crds",
		},
		dev.ClusterHelmInstall{
			RepoName:    "traefik",
			RepoURL:     "https://traefik.github.io/charts",
			PackageName: "traefik",
			Namespace:   "traefik-v2",
			ReleaseName: "traefik",
		},
		dev.ClusterHelmInstall{
			RepoName:    "cnpg",
			RepoURL:     "https://cloudnative-pg.github.io/charts",
			PackageName: "cloudnative-pg",
			Namespace:   "cnpg-system",
			ReleaseName: "cnpg",
			SetVars:     []string{},
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

	devEnvironment = dev.NewEnvironment(
		"stackdome-cluster-agent-dev",
		path.Join(cacheDir, "dev-env"),
		dev.WithClusterOptions([]dev.ClusterOption{
			dev.WithWaitOptions([]dev.WaitOption{
				dev.WithTimeout(10 * time.Minute),
			}),
			dev.WithSchemeBuilder(k8sruntime.SchemeBuilder{
				buildsv1alpha1.AddToScheme,
				corev1alpha1.AddToScheme,
				registryv1alpha1.AddToScheme,
				storagev1alpha1.AddToScheme,
				usersv1alpha1.AddToScheme,
				clientgoscheme.AddToScheme,
			}),
		}),
		dev.WithContainerRuntime(containerRuntime),
		dev.WithKindClusterConfig(kindv1alpha4.Cluster{
			Nodes: []kindv1alpha4.Node{
				{
					Role: kindv1alpha4.ControlPlaneRole,
				},
				{
					Role: kindv1alpha4.WorkerRole,
				},
				{
					Role: kindv1alpha4.WorkerRole,
				},
			},
		}),
		clusterInitializers,
	)
	return nil
}

func (d Dev) Setup(ctx context.Context) error {
	if err := d.init(); err != nil {
		return err
	}

	if err := devEnvironment.Init(ctx); err != nil {
		return fmt.Errorf("initializing dev environment: %w", err)
	}
	return nil
}

func (d Dev) Teardown(ctx context.Context) error {
	if err := d.init(); err != nil {
		return err
	}

	if err := devEnvironment.Destroy(ctx); err != nil {
		return fmt.Errorf("tearing down dev environment: %w", err)
	}
	return nil
}

// Deploy the Addon Operator, and additionally the Mock API Server and Addon Operator webhooks if the respective
// environment variables are set.
// All components are deployed via static manifests.
func (d Dev) Deploy(ctx context.Context) error {
	mg.Deps(
		Dev.Setup, // setup is a pre-requesite and needs to run before we can load images.
	)

	mg.Deps(
		mg.F(Dev.LoadImage, "cluster-agent-manager"),
	)

	if err := d.deploy(ctx, devEnvironment.Cluster); err != nil {
		return fmt.Errorf("deploying: %w", err)
	}
	return nil
}

func (d Dev) LoadImage(image string) error {
	mg.Deps(
		mg.F(Build.ImageBuild, image),
	)

	imageTar := path.Join(cacheDir, "image", image+".tar")
	if err := devEnvironment.LoadImageFromTar(imageTar); err != nil {
		return fmt.Errorf("load image from tar: %w", err)
	}
	return nil
}

// Deploy cluster agent manager to the cluster.
func (d Dev) deploy(
	ctx context.Context, cluster *dev.Cluster,
) error {
	return d.deployClusterAgentManager(ctx, cluster)
}

// deploy the Addon Operator Manager from local files.
func (d Dev) deployClusterAgentManager(ctx context.Context, cluster *dev.Cluster) error {
	deployment := &appsv1.Deployment{}
	err := dev.LoadAndUnmarshalIntoObject("config/deploy/deployment.yaml.tpl", deployment)
	if err != nil {
		return fmt.Errorf("loading cluster-agenet-manager deployment.yaml.tpl: %w", err)
	}

	// Replace image
	patchDeployment(deployment, "cluster-agent-manager", "manager")

	ctx = logr.NewContext(ctx, logger)

	// Deploy deps
	if err := cluster.CreateAndWaitFromFiles(ctx, []string{
		// TODO: replace with CreateAndWaitFromFolders when deployment.yaml is gone.
		"config/deploy/00-namespace.yaml",
		"config/deploy/01-rbac.yaml",
	}); err != nil {
		return fmt.Errorf("deploy cluster-agent-manager dependencies: %w", err)
	}

	// deploy crds
	if err := cluster.CreateAndWaitFromFolders(ctx, []string{
		"config/deploy/crds",
	}); err != nil {
		return fmt.Errorf("deploy cluster-agent-manager crds: %w", err)
	}

	// Delete existing deployment so the new image is picked up.
	// CreateAndWaitForReadiness uses Create which is a no-op on AlreadyExists,
	// so a rebuilt image with the same tag would never roll out.
	existing := &appsv1.Deployment{}
	existing.Name = deployment.Name
	existing.Namespace = deployment.Namespace
	if err := cluster.CtrlClient.Delete(ctx, existing); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("deleting existing cluster-agent-manager deployment: %w", err)
	}

	if err := cluster.CreateAndWaitForReadiness(ctx, deployment); err != nil {
		return fmt.Errorf("deploy cluster-agent-manager: %w", err)
	}
	return nil
}

// Replaces `container`'s image and disables metrics TLS
func patchDeployment(deployment *appsv1.Deployment, name string, container string) {
	image := getImageName(name)

	// replace image
	for i := range deployment.Spec.Template.Spec.Containers {
		containerObj := &deployment.Spec.Template.Spec.Containers[i]

		if containerObj.Name == container {
			containerObj.Image = image
			break
		}
	}
}

func getImageName(name string) string {
	envVar := strings.ToUpper(name) + "_IMAGE"

	var image string
	if len(os.Getenv(envVar)) > 0 {
		image = os.Getenv(envVar)
	} else {
		image = imageURL(name)
	}
	return image
}

func loadAndConvertIntoObject(scheme *k8sruntime.Scheme, filePath string, out interface{}) error {
	objs, err := dev.LoadKubernetesObjectsFromFile(filePath)
	if err != nil {
		return fmt.Errorf("loading object from file: %w", err)
	}
	if err := scheme.Convert(&objs[0], out, nil); err != nil {
		return fmt.Errorf("converting: %w", err)
	}
	return nil
}

func loadAndUnmarshalIntoObject(filePath string, out interface{}) error {
	obj, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	if err = yaml.Unmarshal(obj, &out); err != nil {
		return err
	}
	return nil
}
