package postgrescluster

import (
	"context"
	"fmt"
	"time"

	cnpgv1 "github.com/cloudnative-pg/cloudnative-pg/api/v1"
	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	addonsv1alpha1 "stackdome.io/cluster-agent/api/addons/v1alpha1"
)

const (
	postgresClusterController = "addons-postgrescluster-controller"
	cacheFinalizer            = "addons-postgrescluster.stackdome.io/cache"
)

type subReconcilerResult struct {
	resultNil          bool
	resultStop         bool
	resultRequeue      bool
	resultRequeueAfter *time.Duration
}

var (
	resultNil          = subReconcilerResult{resultNil: true}
	resultStop         = subReconcilerResult{resultStop: true}
	resultRequeue      = subReconcilerResult{resultRequeue: true}
	resultRequeueAfter = func(t time.Duration) subReconcilerResult {
		return subReconcilerResult{resultRequeueAfter: &t}
	}
)

type subReconciler interface {
	reconcile(context.Context, *addonsv1alpha1.PostgresCluster) (subReconcilerResult, error)
	name() string
}

// PostgresClusterReconciler reconciles a PostgresCluster object
type PostgresClusterReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	subReconcilers []subReconciler
}

type PostgresClusterReconcilerSpec struct {
	Client client.Client
	Scheme *runtime.Scheme
}

func NewPostgresAddonReconciler(spec PostgresClusterReconcilerSpec) *PostgresClusterReconciler {
	reconciler := &PostgresClusterReconciler{
		Client: spec.Client,
		Scheme: spec.Scheme,
		subReconcilers: []subReconciler{
			newResourceValidationReconciler(spec.Client, spec.Scheme),
			newPgClusterReconciler(spec.Client, spec.Scheme),
			newClusterLifecycleReconciler(spec.Client, spec.Scheme),
			newDatabaseReconciler(spec.Client, spec.Scheme),
			newBackupReconciler(spec.Client, spec.Scheme),
		},
	}

	return reconciler
}

func (r *PostgresClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling PostgresCluster", "name", req.Name, "namespace", req.Namespace)

	postgresCluster := &addonsv1alpha1.PostgresCluster{}
	if err := r.Get(ctx, req.NamespacedName, postgresCluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if postgresCluster.DeletionTimestamp != nil {
		if controllerutil.ContainsFinalizer(postgresCluster, cacheFinalizer) {
			if err := r.reconcileDelete(ctx, postgresCluster); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(postgresCluster, cacheFinalizer)
			return ctrl.Result{}, r.Client.Update(ctx, postgresCluster)
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(postgresCluster, cacheFinalizer) {
		controllerutil.AddFinalizer(postgresCluster, cacheFinalizer)
		return ctrl.Result{}, r.Client.Update(ctx, postgresCluster)
	}

	originalStatus := postgresCluster.Status.DeepCopy()

	// Initialize the status and phase.
	r.initializeStatusAndPhase(postgresCluster)

	res, err := r.reconcile(ctx, postgresCluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !equality.Semantic.DeepEqual(originalStatus, postgresCluster.Status.DeepCopy()) {
		fmt.Printf("output diff: %s \n", cmp.Diff(originalStatus.Outputs, postgresCluster.Status.Outputs))
		if err := r.Client.Status().Update(ctx, postgresCluster); err != nil {
			return ctrl.Result{}, err
		}
	}
	return res, nil
}

func (r *PostgresClusterReconciler) initializeStatusAndPhase(resource *addonsv1alpha1.PostgresCluster) {
	resource.Status.ObservedGeneration = resource.Generation
	resource.Status.Phase = addonsv1alpha1.PendingPhase
	cond := meta.FindStatusCondition(resource.Status.Conditions, string(addonsv1alpha1.ClusterReady))
	if cond == nil {
		meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
			Type:               string(addonsv1alpha1.ClusterReady),
			Status:             metav1.ConditionUnknown,
			ObservedGeneration: resource.Generation,
			Reason:             "PostgresClusterStatusUnknown",
			Message:            "PostgresCluster status is unknown",
		})
	}
}

func (r *PostgresClusterReconciler) reconcile(ctx context.Context, resource *addonsv1alpha1.PostgresCluster) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling PostgresCluster", "name", resource.Name, "namespace", resource.Namespace)

	// Iterate through all sub-reconcilers and call their reconcile method
	for _, subReconciler := range r.subReconcilers {
		logger.Info("running sub-reconciler", "name", subReconciler.name())
		res, err := subReconciler.reconcile(ctx, resource)
		if err != nil {
			logger.Error(err, "Error in sub-reconciler", "name", subReconciler.name())
			return ctrl.Result{}, err
		}
		if res.resultNil {
			continue
		}
		if res.resultStop {
			return ctrl.Result{}, nil
		}
		if res.resultRequeue {
			return ctrl.Result{Requeue: true}, nil
		}
		if res.resultRequeueAfter != nil {
			return ctrl.Result{RequeueAfter: *res.resultRequeueAfter}, nil
		}
	}

	return ctrl.Result{}, nil
}

func (r *PostgresClusterReconciler) reconcileDelete(ctx context.Context, resource *addonsv1alpha1.PostgresCluster) error {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling PostgresCluster deletion", "name", resource.Name, "namespace", resource.Namespace)
	setStatusCondition(resource, addonsv1alpha1.ClusterReady, metav1.ConditionFalse, "Deleted", "PostgresCluster has been deleted")
	setPhase(resource, addonsv1alpha1.DeletingPhase)
	return r.Status().Update(ctx, resource)
}

// SetupWithManager sets up the controller with the Manager.
func (r *PostgresClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&addonsv1alpha1.PostgresCluster{}).
		Named("addons-postgrescluster").
		Owns(&cnpgv1.ScheduledBackup{}).
		Owns(&cnpgv1.Cluster{}).
		Owns(&cnpgv1.Backup{}).
		Owns(&cnpgv1.Database{}).
		Complete(r)
}
