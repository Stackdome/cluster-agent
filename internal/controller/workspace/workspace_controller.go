/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package workspace

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

	"soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

const (
	ownerKey = ".metadata.controller"
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

type subReconciler func(context.Context, *v1alpha1.Workspace) (subReconcilerResult, error)

// WorkspaceReconciler reconciles a Workspace object
type WorkspaceReconciler struct {
	client.Client
	Scheme                 *runtime.Scheme
	subReconcilers         []subReconciler
	workspaceResourceQueue chan event.GenericEvent
}

func (r *WorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.WithValues("workspace", req.NamespacedName.String())
	logger.Info("IN workspace reconciler")
	ctx = controller.ContextWithLogger(ctx, logger)

	workspace := &v1alpha1.Workspace{}
	if err := r.Client.Get(ctx, req.NamespacedName, workspace); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	res, err := r.reconcile(ctx, workspace)
	if err != nil {
		return res, err
	}

	// Enqueue child resources.
	childResources := &v1alpha1.WorkspaceResourceList{}
	if err := r.Client.List(ctx, childResources, client.MatchingFields{ownerKey: req.Name}); err != nil {
		return ctrl.Result{}, err
	}

	for i := range childResources.Items {
		r.workspaceResourceQueue <- event.GenericEvent{Object: &childResources.Items[i]}
	}

	return res, r.Client.Status().Update(ctx, workspace)
}

func (r *WorkspaceReconciler) reconcile(ctx context.Context, workspace *v1alpha1.Workspace) (ctrl.Result, error) {
	for _, reconciler := range r.subReconcilers {
		subReconcilerRes, err := reconciler(ctx, workspace)
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

func reportWorkspaceNotReady(workspace *v1alpha1.Workspace, reason string, msg string) {
	workspace.Status.ObservedGeneration = workspace.Generation
	workspace.Status.Phase = v1alpha1.WorkspacePending
	meta.SetStatusCondition(&workspace.Status.Conditions, v1.Condition{
		Type:               string(v1alpha1.WorkspaceAvailable),
		Status:             v1.ConditionFalse,
		ObservedGeneration: workspace.Generation,
		Reason:             reason,
		Message:            msg,
	})
}

func reportWorkspaceReady(workspace *v1alpha1.Workspace) {
	workspace.Status.ObservedGeneration = workspace.Generation
	workspace.Status.Phase = v1alpha1.WorkspaceReady
	meta.SetStatusCondition(&workspace.Status.Conditions, v1.Condition{
		Type:               string(v1alpha1.WorkspaceAvailable),
		Status:             v1.ConditionTrue,
		ObservedGeneration: workspace.Generation,
		Reason:             "WorkspaceReady",
		Message:            "All workspace resources and storage ready",
	})
}

func NewWorkspaceReconciler(client client.Client, scheme *runtime.Scheme, workspaceResourceQueue chan event.GenericEvent) *WorkspaceReconciler {
	r := &WorkspaceReconciler{
		Client:                 client,
		Scheme:                 scheme,
		workspaceResourceQueue: workspaceResourceQueue,
	}
	r.subReconcilers = []subReconciler{
		r.ReconcileWorkspaceResources,
	}
	return r
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Workspace{}).
		Watches(&v1alpha1.WorkspaceStorage{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.Workspace{})).
		Watches(&v1alpha1.WorkspaceResource{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.Workspace{})).
		Complete(r)
}
