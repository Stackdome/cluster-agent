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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	buildsv1alpha1 "stackdome.io/cluster-agent/api/builds/v1alpha1"
	"stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/internal/controller"
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

	workspaceResource := &v1alpha1.WorkspaceResource{}
	if err := r.Client.Get(ctx, req.NamespacedName, workspaceResource); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	res, err := r.reconcile(ctx, workspaceResource)
	if err != nil {
		return ctrl.Result{}, err
	}
	applicationBuildStatus, err := r.getImageBuildStatus(ctx, workspaceResource)
	if err != nil {
		return ctrl.Result{}, err
	}
	workspaceResource.Status.CurrentBuild = applicationBuildStatus
	return res, r.Client.Status().Update(ctx, workspaceResource)
}

func (r *WorkspaceResourceReconciler) reconcile(ctx context.Context, resource *v1alpha1.WorkspaceResource) (ctrl.Result, error) {
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
	resource.Status.StatusHash = resource.StatusHash()
}

func (r *WorkspaceResourceReconciler) reportWorkspaceResourceReady(resource *v1alpha1.WorkspaceResource) {
	//TODO: Set source hash
	resource.Status.ObservedGeneration = resource.Generation
	if resource.Spec.BuildSpec != nil {
		resource.Status.ImageSourceHash = resource.Spec.BuildSpec.BuildSourceHash
	}
	resource.Status.Phase = v1alpha1.WorkspaceResourcePhaseReady
	meta.SetStatusCondition(&resource.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceResourceStatusAvailable),
		Status:             metav1.ConditionTrue,
		ObservedGeneration: resource.Generation,
		Reason:             "WorkspaceResourceAvailable",
		Message:            "Workspace is ready",
	})
	resource.Status.StatusHash = resource.StatusHash()
}

func (r *WorkspaceResourceReconciler) getImageBuildStatus(ctx context.Context, resource *v1alpha1.WorkspaceResource) (*v1alpha1.BuildStatus, error) {
	if resource.Spec.BuildSpec == nil {
		return nil, nil
	}

	existingImageBuild := &buildsv1alpha1.ImageBuild{}
	if err := r.Client.Get(ctx,
		types.NamespacedName{
			Name:      buildsv1alpha1.ImageBuildName(resource.Name, resource.Spec.BuildSpec.BuildSourceHash),
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
		Name:       existingImageBuild.Name,
		SourceHash: existingImageBuild.Spec.SourceHash,
		ShortHash:  existingImageBuild.Spec.SourceHash[:7],
		Phase:      string(existingImageBuild.Status.Phase),
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
		&registryAuthReconciler{
			client: client,
			scheme: scheme,
		},
		&imageBuildReconciler{
			Client: client,
			scheme: scheme,
		},
		newWorkloadReconciler(client, scheme),
		&svcReconciler{
			Client: client,
			Scheme: scheme,
		},
	}
	w.subReconcilers = subReconcilers
	return w
}

func imageBuildComplete(imageBuild *buildsv1alpha1.ImageBuild) bool {
	availableCond := meta.FindStatusCondition(imageBuild.Status.Conditions, string(buildsv1alpha1.BuildAvailable))
	if availableCond != nil && availableCond.Status == v1.ConditionTrue {
		return true
	}
	return false
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
		Watches(&buildsv1alpha1.ImageBuild{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.WorkspaceResource{})).
		Watches(&corev1.Service{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.WorkspaceResource{})).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.WorkspaceResource{})).
		Watches(&v1alpha1.WorkspaceVolume{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, o client.Object) []reconcile.Request {
				volume := o.(*v1alpha1.WorkspaceVolume)
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
		// WatchesRawSource(&source.Channel{Source: r.RequeueCh}, &handler.EnqueueRequestForObject{}).
		Complete(r)
}
