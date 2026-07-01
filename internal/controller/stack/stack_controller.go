package stack

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
)

type StackReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func NewStackReconciler(client client.Client, scheme *runtime.Scheme) *StackReconciler {
	return &StackReconciler{Client: client, Scheme: scheme}
}

func (r *StackReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("stack", req.NamespacedName.String())
	ctx = controller.ContextWithLogger(ctx, logger)

	stack := &v1alpha1.Stack{}
	if err := r.Client.Get(ctx, req.NamespacedName, stack); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	children := &v1alpha1.StackResourceList{}
	if err := r.Client.List(ctx, children,
		client.InNamespace(stack.Namespace),
		client.MatchingLabels{v1alpha1.LabelStackName: stack.Name},
	); err != nil {
		return ctrl.Result{}, err
	}

	if adopted, err := r.ensureChildOwnership(ctx, stack, children.Items); err != nil {
		return ctrl.Result{}, err
	} else if adopted {
		return ctrl.Result{Requeue: true}, nil
	}

	previousHash := stack.Status.StatusHash
	stack.Status = aggregateStackStatus(stack, children.Items)
	stack.Status.StatusHash = stack.StatusHash()

	if stack.Status.StatusHash == previousHash {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, r.Client.Status().Update(ctx, stack)
}

// ensureChildOwnership adopts StackResources that match by label but are not
// owned by this Stack. Returns true if any child was adopted, signalling the
// caller to requeue so the watch picks up future changes.
func (r *StackReconciler) ensureChildOwnership(ctx context.Context, stack *v1alpha1.Stack, children []v1alpha1.StackResource) (bool, error) {
	logger := controller.LoggerFromContext(ctx)
	adopted := false
	for i := range children {
		child := &children[i]
		if metav1.IsControlledBy(child, stack) {
			continue
		}
		if err := controllerutil.SetControllerReference(stack, child, r.Scheme); err != nil {
			return adopted, err
		}
		if err := r.Client.Update(ctx, child); err != nil {
			return adopted, err
		}
		logger.Info("adopted child StackResource", "stackResource", child.Name)
		adopted = true
	}
	return adopted, nil
}

func (r *StackReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Stack{}).
		Watches(&storagev1alpha1.Storage{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.Stack{})).
		Watches(&v1alpha1.StackResource{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.Stack{})).
		Complete(r)
}
