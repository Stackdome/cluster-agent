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

package workspacestorage

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

const (
	DefaultRequeueTime = time.Second * 5
	ownerKey           = ".metadata.controller"
)

type subReconciler interface {
	reconcile(context.Context, *v1alpha1.WorkspaceStorage) (subReconcilerResult, error)
}

type WorkspaceStorageReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	subReconcilers []subReconciler
}

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

func (r *WorkspaceStorageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger = logger.WithValues("workspacestorage", req.NamespacedName.String())
	ctx = controller.ContextWithLogger(ctx, logger)
	workspaceState := &v1alpha1.WorkspaceStorage{}

	err := r.Client.Get(ctx, req.NamespacedName, workspaceState)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	return r.reconcile(ctx, workspaceState)
}

func (r *WorkspaceStorageReconciler) reconcile(ctx context.Context, workspaceStorage *v1alpha1.WorkspaceStorage) (ctrl.Result, error) {
	// Assume storageclass already present
	// TODO: Automate this too
	for _, reconciler := range r.subReconcilers {
		subReconcilerRes, err := reconciler.reconcile(ctx, workspaceStorage)
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

	return ctrl.Result{}, r.Client.Status().Update(ctx, workspaceStorage)
}

func NewWorkspaceStorageReconciler(client client.Client, uncachedClient client.Client, scheme *runtime.Scheme) *WorkspaceStorageReconciler {
	subReconcilers := []subReconciler{
		&workspaceVolumeReconciler{
			Client: client,
			Scheme: scheme,
		},
		&userSShKeySecretReconciler{
			Client:         client,
			UncachedClient: uncachedClient,
			Scheme:         scheme,
		},
		&storageServerReconciler{
			Client: client,
			Scheme: scheme,
		},
		&serviceReconciler{
			Client: client,
			Scheme: scheme,
		},
	}
	return &WorkspaceStorageReconciler{
		Client:         client,
		Scheme:         scheme,
		subReconcilers: subReconcilers,
	}
}

func reportWorkspaceStorageUnAvailable(workspaceStorage *v1alpha1.WorkspaceStorage, reason string, msg string, msgArgs ...any) {
	workspaceStorage.Status.Phase = v1alpha1.WSPending
	meta.SetStatusCondition(&workspaceStorage.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceStorageAvailable),
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            fmt.Sprintf(msg, msgArgs...),
		ObservedGeneration: workspaceStorage.Generation,
	})
}

func reportWorkspaceStorageAvailable(workspaceStorage *v1alpha1.WorkspaceStorage, storageSvc *corev1.Service) error {
	workspaceStorage.Status.Phase = v1alpha1.WSReady
	workspaceStorage.Status.ServiceName = storageSvc.Name
	meta.SetStatusCondition(&workspaceStorage.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceStorageAvailable),
		Status:             metav1.ConditionTrue,
		Reason:             "AllComponentsUp",
		ObservedGeneration: workspaceStorage.Generation,
	})

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceStorageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1alpha1.WorkspaceVolume{}, ownerKey, func(rawObj client.Object) []string {
		wsVolume := rawObj.(*v1alpha1.WorkspaceVolume)
		owner := metav1.GetControllerOf(wsVolume)
		return []string{owner.Name}
	}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.WorkspaceStorage{}).
		Watches(&corev1.Service{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.WorkspaceStorage{})).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.WorkspaceStorage{})).
		Watches(&v1alpha1.WorkspaceVolume{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &v1alpha1.WorkspaceStorage{})).
		Complete(r)
}
