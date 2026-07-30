package workload

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
	"stackdome.io/cluster-agent/pkg/portcheck"
)

const DefaultRequeueTime = 5 * time.Second

type DependencyChecker interface {
	DependenciesAvailable(ctx context.Context, resource *v1alpha1.StackResource) (bool, string, error)
	VolumeMountsReadyForUse(ctx context.Context, resource *v1alpha1.StackResource) (bool, string, error)
}

type StatusReporter interface {
	ReportReady(r *v1alpha1.StackResource)
	ReportNotReady(r *v1alpha1.StackResource, reason, msg string)
	ReportFailed(r *v1alpha1.StackResource, reason, msg string)
	SetCondition(r *v1alpha1.StackResource, condType v1alpha1.StackResourceStatusCondition, ready bool, reason, msg string)
}

type Reconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	DependencyChecker DependencyChecker
	UncachedClient    client.Client
	Status            StatusReporter
	// PortVerifier proves that a workload actually listens on the ports it
	// declared. It is optional: a nil verifier disables the check rather than
	// failing the reconcile, so tests and reduced deployments still work.
	PortVerifier *portcheck.Verifier
	// PortCheckGrace bounds how long an unverified resource keeps requeueing
	// while it waits for an answer. Zero means DefaultPortCheckGrace.
	PortCheckGrace time.Duration

	// portCheckRetried records the verification keys whose closed verdict has
	// already been retried once against a workload the kubelet probe has since
	// proven up. It exists because the first dial can land while a slow-booting
	// app is still binding, and that premature verdict must not become the
	// resource's permanent truth. Entries are dropped as soon as a port
	// verifies open.
	portCheckMu      sync.Mutex
	portCheckRetried map[portcheck.Key]struct{}
}

func NewReconciler(
	c client.Client,
	scheme *runtime.Scheme,
	dep DependencyChecker,
	uncached client.Client,
	status StatusReporter,
	portVerifier *portcheck.Verifier,
	portCheckGrace time.Duration,
) *Reconciler {
	return &Reconciler{
		Client:            c,
		Scheme:            scheme,
		DependencyChecker: dep,
		UncachedClient:    uncached,
		Status:            status,
		PortVerifier:      portVerifier,
		PortCheckGrace:    portCheckGrace,
	}
}

// Reconcile runs the pre-flight checks (dependencies, volume mounts, image
// build) and then dispatches to the workload-type-specific reconciler.
func (r *Reconciler) Reconcile(ctx context.Context, resource *v1alpha1.StackResource) (controller.SubReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("in workload reconciler", "resource", resource.Name)

	// --- Gate: sibling dependencies ---
	canRun, message, err := r.DependencyChecker.DependenciesAvailable(ctx, resource)
	if err != nil {
		return controller.ResultNil, err
	}
	if !canRun {
		r.Status.SetCondition(resource, v1alpha1.StackResourceDependenciesReady, false, "DependenciesNotReady", message)
		r.Status.ReportNotReady(resource, "DependenciesNotReady", message)
		return controller.ResultRequeueAfter(DefaultRequeueTime), nil
	}

	// --- Gate: volume mounts ---
	volumeMountsReady, message, err := r.DependencyChecker.VolumeMountsReadyForUse(ctx, resource)
	if err != nil {
		logger.Error(err, "failed to check if volume mounts are ready for use")
	}
	if !volumeMountsReady {
		r.Status.SetCondition(resource, v1alpha1.StackResourceDependenciesReady, false, "VolumeMountsNotReady", message)
		r.Status.ReportNotReady(resource, "VolumeMountsNotReady", message)
		return controller.ResultRequeueAfter(DefaultRequeueTime), nil
	}

	r.Status.SetCondition(resource, v1alpha1.StackResourceDependenciesReady, true, "DependenciesReady", "dependencies and volume mounts ready")

	// --- Gate: image build (only for build-from-source resources) ---
	if resource.Spec.BuildSpec != nil {
		currentApplicationBuild, err := r.getImageBuild(ctx, resource)
		if err != nil {
			return controller.ResultNil, err
		}
		if !imageBuildComplete(currentApplicationBuild) {
			r.Status.SetCondition(resource, v1alpha1.StackResourceBuildReady, false, "BuildNotReady", "application build is not yet ready")
			r.Status.ReportNotReady(resource, "ApplicationBuildNotYetReady", "Application build is not yet ready")
			return controller.ResultStop, nil
		}
		r.Status.SetCondition(resource, v1alpha1.StackResourceBuildReady, true, "BuildReady", "application build complete")
	}

	// --- Dispatch by workload type ---
	switch resource.Spec.WorkloadType {
	case v1alpha1.WorkloadTypeService, v1alpha1.WorkloadTypeWorker, "":
		return r.reconcileDeployment(ctx, resource)
	case v1alpha1.WorkloadTypeStatefulService:
		return r.reconcileStatefulSet(ctx, resource)
	case v1alpha1.WorkloadTypeJob:
		return r.reconcileJob(ctx, resource)
	case v1alpha1.WorkloadTypeCronJob:
		return r.reconcileCronJob(ctx, resource)
	default:
		return controller.ResultNil, fmt.Errorf("unknown workload type: %s", resource.Spec.WorkloadType)
	}
}
