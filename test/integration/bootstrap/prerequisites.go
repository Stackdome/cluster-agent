package bootstrap

import (
	"context"
	"fmt"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	TestNamespace    = "pg-integration-test"
	ImageCatalogName = "postgres-catalog"
	PostgreSQLImage  = "ghcr.io/cloudnative-pg/postgresql:16"
	PostgreSQLMajor  = 16
)

type PrerequisiteManager struct {
	client client.Client
	logger logr.Logger
}

func NewPrerequisiteManager(c client.Client, logger logr.Logger) *PrerequisiteManager {
	return &PrerequisiteManager{client: c, logger: logger}
}

func (pm *PrerequisiteManager) Setup(ctx context.Context) error {
	pm.logger.Info("Setting up test prerequisites")

	// Create test namespace
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: TestNamespace},
	}
	if err := pm.client.Create(ctx, ns); err != nil {
		return fmt.Errorf("creating test namespace: %w", err)
	}

	// Create ImageCatalog with PostgreSQL 16
	imageCatalog := &cnpgv1.ImageCatalog{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ImageCatalogName,
			Namespace: TestNamespace,
		},
		Spec: cnpgv1.ImageCatalogSpec{
			Images: []cnpgv1.CatalogImage{
				{
					Major: PostgreSQLMajor,
					Image: PostgreSQLImage,
				},
			},
		},
	}
	if err := pm.client.Create(ctx, imageCatalog); err != nil {
		return fmt.Errorf("creating image catalog: %w", err)
	}

	pm.logger.Info("Prerequisites created", "namespace", TestNamespace, "imageCatalog", ImageCatalogName)
	return nil
}
