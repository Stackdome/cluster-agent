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

package workspaceresource

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/strings/slices"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

type subReconciler interface {
	reconcile(context.Context, *v1alpha1.WorkspaceResource) (subReconcilerResult, error)
}

const DefaultRequeueTime = 5 * time.Second

// WorkspaceResourceReconciler reconciles a WorkspaceResource object
type WorkspaceResourceReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	subReconcilers []subReconciler
	RequeueCh      chan event.GenericEvent
}

func (r *WorkspaceResourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger = logger.WithValues("workspaceResource:", req.String())
	logger.Info("in workspace resource reconciler")
	ctx = controller.ContextWithLogger(ctx, logger)

	workspaceService := &v1alpha1.WorkspaceResource{}
	if err := r.Client.Get(ctx, req.NamespacedName, workspaceService); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	res, err := r.reconcile(ctx, workspaceService)
	if err != nil {
		return ctrl.Result{}, err
	}

	return res, r.Client.Status().Update(ctx, workspaceService)
}

func (r *WorkspaceResourceReconciler) reconcile(ctx context.Context, resource *v1alpha1.WorkspaceResource) (ctrl.Result, error) {
	canRun, err := r.dependenciesAvailable(ctx, resource)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !canRun {
		// Our dependencies are not yet ready, we will run when our dependencies are available.
		reportWorkspaceResourceNotReady(resource, "DependenciesNotReady", "Dependent resources are not yet ready")
		// We need to requeue this request because we dont get requeued automatically when the other dependencies are
		// ready/updated.
		return ctrl.Result{RequeueAfter: DefaultRequeueTime}, nil
	}

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
	}
	return ctrl.Result{}, nil
}

func reportWorkspaceResourceNotReady(resource *v1alpha1.WorkspaceResource, reason, msg string) {
	resource.Status.ObservedGeneration = resource.Generation
	resource.Status.Phase = v1alpha1.WorkspaceResourcePhasePending
	resource.Status.ExternalAddress = nil
	resource.Status.InternalAddress = nil
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceResourceStatusAvailable),
		Status:             metav1.ConditionFalse,
		ObservedGeneration: resource.Generation,
		Reason:             reason,
		Message:            msg,
	})
}

func (r *WorkspaceResourceReconciler) reportWorkspaceResourceReady(resource *v1alpha1.WorkspaceResource) {
	//TODO: Set source hash
	resource.Status.ObservedGeneration = resource.Generation
	if resource.Spec.ApplicationSourceSpec != nil {
		resource.Status.ImageSourceHash = resource.Spec.ApplicationSourceSpec.BuildSourceHash
	}
	resource.Status.Phase = v1alpha1.WorkspaceResourcePhaseReady
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceResourceStatusAvailable),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: resource.Generation,
		Reason:             "WorkspaceResourceAvailable",
		Message:            "Workspace is ready",
	})
}

func (r *WorkspaceResourceReconciler) getDependencies(ctx context.Context, resource *v1alpha1.WorkspaceResource) ([]v1alpha1.WorkspaceResource, error) {
	if len(resource.Spec.DependsOn) == 0 {
		return nil, nil
	}
	wrList := &v1alpha1.WorkspaceResourceList{}
	workspaceRef := metav1.GetControllerOf(resource)
	if workspaceRef == nil {
		return nil, fmt.Errorf("missing owner ref for workspace resource")
	}
	if err := r.Client.List(ctx, wrList, client.InNamespace(resource.Namespace), client.MatchingFields{
		ownerKey: workspaceRef.Name,
	}); err != nil {
		return nil, err
	}
	dependsOn := resource.Spec.DependsOn

	// for _, dep := range resource.Spec.DependsOn {
	// 	dependsOn = append(dependsOn, resource.Name)
	// }
	res := make([]v1alpha1.WorkspaceResource, 0)
	for _, wr := range wrList.Items {
		if slices.Contains(dependsOn, wr.Name) {
			res = append(res, wr)
		}
	}
	return res, nil
}

func (r *WorkspaceResourceReconciler) dependenciesAvailable(ctx context.Context, resource *v1alpha1.WorkspaceResource) (bool, error) {
	if len(resource.Spec.DependsOn) == 0 {
		return true, nil
	}
	dependencyList, err := r.getDependencies(ctx, resource)
	logger := controller.LoggerFromContext(ctx)
	logger.Info(fmt.Sprintf("deps: %+v, len: %d", dependencyList, len(dependencyList)))
	if err != nil {
		return false, err
	}
	if len(dependencyList) != len(resource.Spec.DependsOn) {
		return false, fmt.Errorf("some dependency services are not yet created")
	}

	unreadyDeps := []*v1alpha1.WorkspaceResource{}
	for i := range dependencyList {
		currentDep := dependencyList[i]
		if !workspaceAvailable(&currentDep) {
			unreadyDeps = append(unreadyDeps, &currentDep)
		}
	}
	if len(unreadyDeps) > 0 {
		return false, nil
	}
	return true, nil
}

func workspaceAvailable(resource *v1alpha1.WorkspaceResource) bool {
	availableCond := meta.FindStatusCondition(resource.Status.Conditions, string(v1alpha1.WorkspaceResourceStatusAvailable))
	if availableCond != nil &&
		availableCond.Status == metav1.ConditionTrue &&
		availableCond.ObservedGeneration == resource.Generation {
		return true
	}
	return false
}

func NewWorkspaceResourceReconciler(client client.Client, scheme *runtime.Scheme) *WorkspaceResourceReconciler {
	w := &WorkspaceResourceReconciler{
		Client:    client,
		Scheme:    scheme,
		RequeueCh: make(chan event.GenericEvent),
	}
	subReconcilers := []subReconciler{
		&workspaceResourceBuildReconciler{
			Client: client,
			Scheme: scheme,
		},
		&workloadReconciler{
			Client:                      client,
			Scheme:                      scheme,
			workspaceResourceReconciler: w,
		},
		&svcReconciler{
			Client: client,
			Scheme: scheme,
		},
	}
	w.subReconcilers = subReconcilers
	return w
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceResourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1alpha1.WorkspaceResource{}, ownerKey, func(rawObj client.Object) []string {
		wr := rawObj.(*v1alpha1.WorkspaceResource)
		owner := metav1.GetControllerOf(wr)
		return []string{owner.Name}
	}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.WorkspaceResource{}).
		Watches(&v1alpha1.WorkspaceApplicationBuild{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.WorkspaceResource{})).
		Watches(&corev1.Service{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.WorkspaceResource{})).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.WorkspaceResource{})).
		WatchesRawSource(&source.Channel{Source: r.RequeueCh}, &handler.EnqueueRequestForObject{}).
		Complete(r)
}
