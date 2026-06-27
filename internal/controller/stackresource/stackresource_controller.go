package stackresource

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

type subReconcilerResult struct {
	resultNil            bool
	resultStop           bool
	resultRequeue        bool
	resultRequeueAfter   *time.Duration
	deferredRequeueAfter *time.Duration
}

var (
	resultNil          = subReconcilerResult{resultNil: true}
	resultStop         = subReconcilerResult{resultStop: true}
	resultRequeue      = subReconcilerResult{resultRequeue: true}
	resultRequeueAfter = func(t time.Duration) subReconcilerResult {
		return subReconcilerResult{resultRequeueAfter: &t}
	}
	resultContinue        = resultNil // alias: continue to next sub-reconciler
	resultDeferredRequeue = func(t time.Duration) subReconcilerResult {
		return subReconcilerResult{deferredRequeueAfter: &t}
	}
)

type subReconciler interface {
	reconcile(context.Context, *v1alpha1.StackResource) (subReconcilerResult, error)
}

const DefaultRequeueTime = 5 * time.Second

// StackResourceReconciler reconciles a StackResource object
type StackResourceReconciler struct {
	client.Client
	uncachedClient client.Client
	Scheme         *runtime.Scheme
	subReconcilers []subReconciler
}

func (r *StackResourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger = logger.WithValues("stackResource:", req.String())
	logger.Info("in stack resource reconciler")
	ctx = controller.ContextWithLogger(ctx, logger)
	stackResource := &v1alpha1.StackResource{}
	if err := r.Client.Get(ctx, req.NamespacedName, stackResource); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	originalStatus := stackResource.Status.DeepCopy()

	// Initialize the status and phase of the stack resource
	r.initializeStatusAndPhase(stackResource)
	res, err := r.reconcile(ctx, stackResource)
	if err != nil {
		return ctrl.Result{}, err
	}
	applicationBuildStatus, err := r.getImageBuildStatus(ctx, stackResource)
	if err != nil {
		return ctrl.Result{}, err
	}
	stackResource.Status.CurrentBuild = applicationBuildStatus

	if !equality.Semantic.DeepEqual(originalStatus, &stackResource.Status) {
		logger.Info("updating stack resource status")
		if err := r.Client.Status().Update(ctx, stackResource); err != nil {
			return ctrl.Result{}, err
		}
	}

	return res, nil
}

func (r *StackResourceReconciler) initializeStatusAndPhase(resource *v1alpha1.StackResource) {
	resource.Status.ObservedGeneration = resource.Generation
	resource.Status.Phase = v1alpha1.StackResourcePhasePending
	resource.Status.ExternalAddress = nil // this is actually a no-op; maybe skip?
	resource.Status.InternalAddress = nil // same here
	resource.Status.ImageSourceRevision = ""
	resource.Status.CurrentBuild = nil
	// NOTE: LastFailureDetails is intentionally NOT cleared here. It persists
	// across reconciles and is managed by the workload reconciler.
	cond := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceStatusAvailable))
	if cond == nil {
		meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
			Type:               string(v1alpha1.StackResourceStatusAvailable),
			Status:             metav1.ConditionUnknown,
			ObservedGeneration: resource.Generation,
			Reason:             "StackResourceStausUnknown",
			Message:            "StackResource status is unknown",
		})
	}
}

func (r *StackResourceReconciler) reconcile(ctx context.Context, resource *v1alpha1.StackResource) (ctrl.Result, error) {
	var deferredRequeue *time.Duration
	for _, subReconciler := range r.subReconcilers {
		subReconcilerRes, err := subReconciler.reconcile(ctx, resource)
		switch {
		case err != nil:
			return ctrl.Result{}, err
		case subReconcilerRes == resultStop:
			return ctrl.Result{}, nil
		case subReconcilerRes == resultRequeue:
			return ctrl.Result{Requeue: true}, nil
		case subReconcilerRes.resultRequeueAfter != nil:
			return ctrl.Result{RequeueAfter: *subReconcilerRes.resultRequeueAfter}, nil
		}
		if subReconcilerRes.deferredRequeueAfter != nil {
			deferredRequeue = subReconcilerRes.deferredRequeueAfter
		}
	}
	if deferredRequeue != nil {
		return ctrl.Result{RequeueAfter: *deferredRequeue}, nil
	}
	return ctrl.Result{}, nil
}

func reportStackResourceNotReady(resource *v1alpha1.StackResource, reason, msg string) {
	objectRevision, ok := resource.Annotations[v1alpha1.RevisionAnnotation]
	if ok {
		resource.Status.ObservedRevision = objectRevision
	}
	resource.Status.ObservedGeneration = resource.Generation
	resource.Status.Phase = v1alpha1.StackResourcePhasePending
	resource.Status.ExternalAddress = nil
	resource.Status.InternalAddress = nil
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.StackResourceStatusAvailable),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: resource.Generation,
		Reason:             reason,
		Message:            msg,
	})
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.StackResourceConverged),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: resource.Generation,
		Reason:             reason,
		Message:            msg,
	})
	resource.Status.StatusHash = resource.StatusHash()
}

func setResourceCondition(resource *v1alpha1.StackResource, condType v1alpha1.StackResourceStatusCondition, ready bool, reason, msg string) {
	condStatus := metav1.ConditionFalse
	if ready {
		condStatus = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(condType),
		Status:             condStatus,
		ObservedGeneration: resource.Generation,
		Reason:             reason,
		Message:            msg,
	})
}

func reportStackResourceFailed(resource *v1alpha1.StackResource, reason, msg string) {
	resource.Status.ObservedGeneration = resource.Generation
	if rev, ok := resource.Annotations[v1alpha1.RevisionAnnotation]; ok {
		resource.Status.ObservedRevision = rev
	}
	resource.Status.Phase = v1alpha1.StackResourcePhaseFailed
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.StackResourceStatusAvailable),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: resource.Generation,
		Reason:             reason,
		Message:            msg,
	})
	// Terminal failure: mark Stalled so the Stack controller can distinguish
	// retriable (Pending) from non-retriable (Failed) children.
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.StackResourceStalled),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: resource.Generation,
		Reason:             reason,
		Message:            msg,
	})
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.StackResourceConverged),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: resource.Generation,
		Reason:             reason,
		Message:            msg,
	})
	resource.Status.StatusHash = resource.StatusHash()
}

func reportStackResourceReady(resource *v1alpha1.StackResource) {
	resource.Status.ObservedGeneration = resource.Generation
	if resource.Spec.BuildSpec != nil {
		resource.Status.ImageSourceRevision = resource.Spec.BuildSpec.SourceRevision.GetSourceRevisionString()
	}
	if rev, ok := resource.Annotations[v1alpha1.RevisionAnnotation]; ok {
		resource.Status.ObservedRevision = rev
	}

	notConverged := !isResourceConverged(resource)

	if notConverged {
		resource.Status.Phase = v1alpha1.StackResourcePhaseDegraded
	} else {
		resource.Status.Phase = v1alpha1.StackResourcePhaseReady
	}

	availableMsg := "StackResource is available"
	if notConverged {
		availableMsg = "available on previous revision; current rollout not converged"
	}

	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.StackResourceStatusAvailable),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: resource.Generation,
		Reason:             "StackResourceAvailable",
		Message:            availableMsg,
	})
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.StackResourceStalled),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: resource.Generation,
		Reason:             "StackResourceAvailable",
		Message:            "StackResource is available",
	})
	resource.Status.StatusHash = resource.StatusHash()
}

func isResourceConverged(resource *v1alpha1.StackResource) bool {
	cond := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceConverged))
	return cond != nil && cond.Status == metav1.ConditionTrue && cond.ObservedGeneration == resource.Generation
}

func (r *StackResourceReconciler) getImageBuildStatus(ctx context.Context, resource *v1alpha1.StackResource) (*v1alpha1.BuildStatus, error) {
	if resource.Spec.BuildSpec == nil {
		return nil, nil
	}

	existingImageBuild := &buildsv1alpha1.ImageBuild{}
	if err := r.Client.Get(ctx,
		types.NamespacedName{
			Name:      buildsv1alpha1.ImageBuildName(resource.Name, resource.Spec.BuildSpec.SourceRevision.GetSourceRevisionString()),
			Namespace: resource.Namespace,
		},
		existingImageBuild,
	); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	res := &v1alpha1.BuildStatus{
		Name:           existingImageBuild.Name,
		SourceRevision: existingImageBuild.Status.BuildSourceRevision,
		Phase:          string(existingImageBuild.Status.Phase),
	}

	availableCond := meta.FindStatusCondition(existingImageBuild.Status.Conditions, string(buildsv1alpha1.BuildAvailable))
	if availableCond != nil {
		res.Available = availableCond.Status == metav1.ConditionTrue
		res.Message = availableCond.Message
		res.Reason = availableCond.Reason
	} else {
		res.Available = false
	}
	return res, nil
}

func stackResourceAvailable(resource *v1alpha1.StackResource) bool {
	availableCond := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.StackResourceStatusAvailable))
	if availableCond != nil &&
		availableCond.Status == metav1.ConditionTrue &&
		availableCond.ObservedGeneration == resource.Generation {
		return true
	}
	return false
}

func NewStackResourceReconciler(client client.Client, scheme *runtime.Scheme, uncachedClient client.Client, imageBuildHistoryLimit int) *StackResourceReconciler {
	w := &StackResourceReconciler{
		Client: client,
		Scheme: scheme,
	}

	depChecker := &workloadDependencyChecker{
		Client: client,
	}
	subReconcilers := []subReconciler{
		&registryAuthReconciler{
			client: client,
			scheme: scheme,
		},
		&imageBuildReconciler{
			Client:       client,
			scheme:       scheme,
			historyLimit: imageBuildHistoryLimit,
		},
		&workloadReconciler{
			Client:            client,
			Scheme:            scheme,
			DependencyChecker: depChecker,
			uncachedClient:    uncachedClient,
		},
		&svcReconciler{
			Client: client,
			Scheme: scheme,
		},
	}
	w.subReconcilers = subReconcilers
	return w
}

func imageBuildFailed(imageBuild *buildsv1alpha1.ImageBuild) bool {
	return imageBuild.Status.Phase == buildsv1alpha1.BuildPhaseFailed
}

func imageBuildComplete(imageBuild *buildsv1alpha1.ImageBuild) bool {
	availableCond := meta.FindStatusCondition(imageBuild.Status.Conditions, string(buildsv1alpha1.BuildAvailable))
	if availableCond != nil && availableCond.Status == metav1.ConditionTrue {
		return true
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *StackResourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.StackResource{}).
		Watches(&buildsv1alpha1.ImageBuild{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.StackResource{})).
		Watches(&corev1.Service{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.StackResource{})).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.StackResource{})).
		Watches(&storagev1alpha1.Volume{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, o client.Object) []reconcile.Request {
				volume := o.(*storagev1alpha1.Volume)
				res := []reconcile.Request{}
				if volume.Spec.Source != nil && len(volume.Spec.Source.BuildArtifacts) != 0 {
					for _, artifact := range volume.Spec.Source.BuildArtifacts {
						res = append(res, reconcile.Request{
							NamespacedName: types.NamespacedName{
								Namespace: volume.Namespace,
								Name:      artifact.BuildSource.Name,
							},
						})
					}
				}
				return res
			},
		)).
		Complete(r)
}
