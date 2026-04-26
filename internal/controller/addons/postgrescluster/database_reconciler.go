package postgrescluster

import (
	"context"
	"fmt"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	k8sapierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
)

const (
	defaultDatabaseOwner = "app"
	defaultDatabaseName  = "app"
	createdByLabel       = "addons.postgres.stackdome.io/created-by"
	createdByValue       = "stackdome-addons-controller"
)

type databaseReconciler struct {
	client client.Client
	scheme *runtime.Scheme
}

func newDatabaseReconciler(client client.Client, scheme *runtime.Scheme) *databaseReconciler {
	return &databaseReconciler{
		client: client,
		scheme: scheme,
	}
}

func (r *databaseReconciler) name() string {
	return "database-reconciler"
}

func (r *databaseReconciler) reconcile(ctx context.Context, resource *addonsv1alpha1.PostgresCluster) (subReconcilerResult, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling databases for PostgresCluster", "name", resource.Name, "namespace", resource.Namespace)
	clusterReadyCond := meta.FindStatusCondition(resource.Status.Conditions, string(addonsv1alpha1.ClusterReady))
	if clusterReadyCond == nil || clusterReadyCond.ObservedGeneration != resource.Generation || clusterReadyCond.Status != metav1.ConditionTrue {
		logger.Info("PostgresCluster is not ready, skipping database reconciliation", "name", resource.Name, "namespace", resource.Namespace)
		return resultNil, nil
	}

	if len(resource.Spec.Databases) == 0 {
		// If no databases are specified, we populate only the default "app" database in the status.
		resource.Status.Outputs.Databases = []addonsv1alpha1.DatabaseInfo{
			{
				Name:  defaultDatabaseName,
				Owner: defaultDatabaseOwner,
			},
		}
		return resultNil, nil
	}

	databasesSpec := resource.Spec.Databases

	for _, dbSpec := range databasesSpec {
		err := r.ensureDatabase(ctx, resource, dbSpec)
		if err != nil {
			logger.Error(err, "Failed to ensure database", "name", dbSpec.Name, "namespace", resource.Namespace)
			return resultNil, err
		}
	}

	if err := r.reconcileUnWantedDatabases(ctx, resource); err != nil {
		return resultNil, err
	}

	for _, dbSpec := range databasesSpec {
		reconciled, msg, err := r.reconcileDatabaseStatus(ctx, resource, dbSpec)
		if err != nil {
			return resultNil, err
		}
		if !reconciled {
			setStatusCondition(resource, addonsv1alpha1.DatabasesApplied, metav1.ConditionFalse, "DatabasesNotApplied", msg)
			return resultStop, nil
		}
	}
	setStatusCondition(resource, addonsv1alpha1.DatabasesApplied, metav1.ConditionTrue, "DatabasesApplied", "All databases and extensions applied successfully")
	populateDatabaseInfoStatus(resource, databasesSpec)
	return resultNil, nil
}

func populateDatabaseInfoStatus(resource *addonsv1alpha1.PostgresCluster, databasesSpec []addonsv1alpha1.DatabaseSpec) {
	desiredDatabaseNames := lo.Map(databasesSpec, func(dbSpec addonsv1alpha1.DatabaseSpec, _ int) string {
		return dbSpec.Name
	})
	desiredDatabaseNames = append(desiredDatabaseNames, defaultDatabaseName)
	desiredDatabaseNames = lo.Uniq(desiredDatabaseNames)
	resource.Status.Outputs.Databases = lo.Map(desiredDatabaseNames, func(name string, _ int) addonsv1alpha1.DatabaseInfo {
		return addonsv1alpha1.DatabaseInfo{
			Name:  name,
			Owner: defaultDatabaseOwner,
		}
	})
}

func (r *databaseReconciler) reconcileDatabaseStatus(ctx context.Context, resource *addonsv1alpha1.PostgresCluster, dbSpec addonsv1alpha1.DatabaseSpec) (bool, string, error) {
	existingDatabase := &cnpgv1.Database{}
	err := r.client.Get(ctx, client.ObjectKey{
		Name:      dbSpec.Name,
		Namespace: resource.Namespace,
	}, existingDatabase)
	if err != nil {
		return false, "", err
	}

	databaseApplied := existingDatabase.Status.Applied != nil && *existingDatabase.Status.Applied && existingDatabase.Status.ObservedGeneration == existingDatabase.Generation

	desiredExtentions := lo.SliceToMap(dbSpec.Extensions, func(ext addonsv1alpha1.ExtensionSpec) (string, struct{}) {
		return string(ext.Name), struct{}{}
	})

	extensionsApplied := true

	for _, existingExt := range existingDatabase.Status.Extensions {
		if _, exists := desiredExtentions[string(existingExt.Name)]; !exists {
			extensionsApplied = false
		} else {
			extensionsApplied = existingExt.Applied
		}
	}

	if !databaseApplied {
		return false, fmt.Sprintf("Database '%s' not applied", dbSpec.Name), nil
	}

	if !extensionsApplied {
		return false, fmt.Sprintf("Extensions for database '%s' not applied", dbSpec.Name), nil
	}

	return true, "Database and extensions are applied", nil
}

func (r *databaseReconciler) reconcileUnWantedDatabases(ctx context.Context, resource *addonsv1alpha1.PostgresCluster) error {
	desiredDatabasesMap := lo.SliceToMap(resource.Spec.Databases, func(db addonsv1alpha1.DatabaseSpec) (string, struct{}) {
		return db.Name, struct{}{}
	})

	databaseList := &cnpgv1.DatabaseList{}
	// Filter by label to find databases created by this controller
	if err := r.client.List(ctx, databaseList, client.InNamespace(resource.Namespace), client.MatchingLabels{
		createdByLabel: createdByValue,
	}); err != nil {
		return err
	}

	for _, existingDatabase := range databaseList.Items {
		if existingDatabase.Spec.Name == defaultDatabaseName {
			// we dont delete the "app" database, it is always present
			continue
		}

		if _, exists := desiredDatabasesMap[existingDatabase.Name]; !exists {
			current := existingDatabase.DeepCopy()
			if current.Spec.Ensure == cnpgv1.EnsureAbsent {
				// delete
				if current.Status.Applied != nil && *current.Status.Applied && current.Status.ObservedGeneration == current.Generation {
					if err := r.client.Delete(ctx, current); err != nil && !k8sapierrors.IsNotFound(err) {
						return err
					}
				}
				// We will get requeued when "EnsureAbsent" is reconciled by cnpg.
				return nil
			} else {
				// First we set EnsureAbsent, then we update the resource. We will get requeued and then we delete the resource in the next reconciliation loop.
				current.Spec.Ensure = cnpgv1.EnsureAbsent
				if err := r.client.Update(ctx, current); err != nil && !k8sapierrors.IsNotFound(err) {
					return err
				}
			}
		}
	}

	return nil
}

func (r *databaseReconciler) ensureDatabase(ctx context.Context, resource *addonsv1alpha1.PostgresCluster, dbSpec addonsv1alpha1.DatabaseSpec) error {
	desiredDatabase := &cnpgv1.Database{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dbSpec.Name,
			Namespace: resource.Namespace,
			Labels: map[string]string{
				createdByLabel: createdByValue,
			},
		},
		Spec: cnpgv1.DatabaseSpec{
			ClusterRef: corev1.LocalObjectReference{
				Name: resource.CnpgClusterName(),
			},
			Name: dbSpec.Name,
			// Currently the owner is always "app", in the future we can consider allowing users to specify the owner in the spec.
			// This means all the databases created in this postgres cluster will have the same owner, which is "app" in this case.
			// There is an enhancement created to address this in the future.
			Owner:      defaultDatabaseOwner,
			Ensure:     cnpgv1.EnsurePresent,
			Extensions: []cnpgv1.ExtensionSpec{},
		},
	}
	for _, ext := range dbSpec.Extensions {
		desiredDatabase.Spec.Extensions = append(desiredDatabase.Spec.Extensions, cnpgv1.ExtensionSpec{
			DatabaseObjectSpec: cnpgv1.DatabaseObjectSpec{
				Name:   string(ext.Name),
				Ensure: cnpgv1.EnsurePresent,
			},
		})
	}

	desiredDatabase.SetGroupVersionKind(cnpgv1.SchemeGroupVersion.WithKind("Database"))
	if err := controllerutil.SetControllerReference(resource, desiredDatabase, r.scheme); err != nil {
		return err
	}

	existingDatabase := &cnpgv1.Database{}
	err := r.client.Get(ctx, client.ObjectKey{
		Name:      dbSpec.Name,
		Namespace: resource.Namespace,
	}, existingDatabase)
	if err != nil {
		if k8sapierrors.IsNotFound(err) {
			return r.client.Create(ctx, desiredDatabase)
		}
		return err
	}

	return nil
}
