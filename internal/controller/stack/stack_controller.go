package stack

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

const (
	ownerKey = ".metadata.owner"
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

type subReconciler func(context.Context, *v1alpha1.Stack) (subReconcilerResult, error)

// StackReconciler reconciles a Stack object
type StackReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	subReconcilers     []subReconciler
	stackResourceQueue chan event.GenericEvent
}

func (r *StackReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.WithValues("stack", req.NamespacedName.String())
	logger.Info("In stack reconciler")
	ctx = controller.ContextWithLogger(ctx, logger)

	stack := &v1alpha1.Stack{}
	if err := r.Client.Get(ctx, req.NamespacedName, stack); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	res, err := r.reconcile(ctx, stack)
	if err != nil {
		return res, err
	}

	// Enqueue child resources.
	childResources := &v1alpha1.StackResourceList{}
	if err := r.Client.List(ctx, childResources, client.MatchingFields{ownerKey: req.Name}); err != nil {
		return ctrl.Result{}, err
	}

	for i := range childResources.Items {
		r.stackResourceQueue <- event.GenericEvent{Object: &childResources.Items[i]}
	}
	return res, r.Client.Status().Update(ctx, stack)
}

func (r *StackReconciler) reconcile(ctx context.Context, stack *v1alpha1.Stack) (ctrl.Result, error) {
	for _, reconciler := range r.subReconcilers {
		subReconcilerRes, err := reconciler(ctx, stack)
		if err != nil {
			return ctrl.Result{}, err
		}
		switch {
		case subReconcilerRes == resultStop:
			return ctrl.Result{}, nil
		case subReconcilerRes == resultRequeue:
			return ctrl.Result{Requeue: true}, nil
		case subReconcilerRes.resultRequeueAfter != nil:
			return ctrl.Result{RequeueAfter: *subReconcilerRes.resultRequeueAfter}, nil
		}
	}
	return ctrl.Result{}, nil
}

func reportStackNotReady(stack *v1alpha1.Stack, reason string, msg string) {
	objectRevision, ok := stack.Annotations[v1alpha1.StackdomeServerObjectRevisionAnnotationKey]
	if ok {
		stack.Status.ObservedStackdomeServerObjectRevision = objectRevision
	}
	stack.Status.ObservedGeneration = stack.Generation
	stack.Status.Phase = v1alpha1.StackPending
	meta.SetStatusCondition(&stack.Status.Conditions, v1.Condition{
		Type:               string(v1alpha1.StackAvailable),
		Status:             v1.ConditionFalse,
		ObservedGeneration: stack.Generation,
		Reason:             reason,
		Message:            msg,
	})
	stack.Status.StatusHash = stack.StatusHash()
}

func reportStackReady(stack *v1alpha1.Stack) {
	objectRevision, ok := stack.Annotations[v1alpha1.StackdomeServerObjectRevisionAnnotationKey]
	if ok {
		stack.Status.ObservedStackdomeServerObjectRevision = objectRevision
	}
	stack.Status.ObservedGeneration = stack.Generation
	stack.Status.Phase = v1alpha1.StackReady
	meta.SetStatusCondition(&stack.Status.Conditions, v1.Condition{
		Type:               string(v1alpha1.StackAvailable),
		Status:             v1.ConditionTrue,
		ObservedGeneration: stack.Generation,
		Reason:             "StackReady",
		Message:            "All stack resources ready",
	})
	stack.Status.StatusHash = stack.StatusHash()
}

func NewStackReconciler(client client.Client, scheme *runtime.Scheme, stackResourceQueue chan event.GenericEvent) *StackReconciler {
	r := &StackReconciler{
		Client:             client,
		Scheme:             scheme,
		stackResourceQueue: stackResourceQueue,
	}
	r.subReconcilers = []subReconciler{
		r.ReconcileStackResources,
	}
	return r
}

// SetupWithManager sets up the controller with the Manager.
func (r *StackReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Stack{}).
		Watches(&storagev1alpha1.Storage{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.Stack{})).
		Watches(&v1alpha1.StackResource{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.Stack{})).
		Complete(r)
}
