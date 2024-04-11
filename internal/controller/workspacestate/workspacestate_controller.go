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

package workspacestate

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"soradev.io/cluster-agent/api/v1alpha1"
	workspacev1alpha1 "soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

const (
	DefaultRequeueTime = time.Minute * 2
)

// WorkspaceStateReconciler reconciles a WorkspaceState object
type WorkspaceStateReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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

func (r *WorkspaceStateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger = logger.WithValues("workspacestate", req.NamespacedName.String())
	ctx = controller.ContextWithLogger(ctx, logger)
	workspaceState := &workspacev1alpha1.WorkspaceState{}

	err := r.Client.Get(ctx, req.NamespacedName, workspaceState)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	return r.reconcile(ctx, workspaceState)
}

func (r *WorkspaceStateReconciler) reconcile(ctx context.Context, workspaceState *workspacev1alpha1.WorkspaceState) (ctrl.Result, error) {
	logger := controller.LoggerFromContext(ctx)

	// Assume storageclass already present
	// TODO: Automate this too
	err := r.reconcilePVCs(ctx, workspaceState)
	if err != nil {
		return ctrl.Result{}, err
	}

	subReconcilerRes, err := r.reconcileStoragePods(ctx, workspaceState)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch {
	case subReconcilerRes == resultStop:
		return ctrl.Result{}, nil
	case subReconcilerRes == resultRequeue:
		return ctrl.Result{RequeueAfter: DefaultRequeueTime}, nil
	case subReconcilerRes.resultRequeueAfter != nil:
		return ctrl.Result{RequeueAfter: *subReconcilerRes.resultRequeueAfter}, nil
	}

	reconcilerRes, svcNodePort, err := r.ensureSVC(ctx, workspaceState)
	logger.Info(fmt.Sprintf("subreconiler res: %#v, svcport: %v, err: %v", reconcilerRes, svcNodePort, err))
	if err != nil {
		return ctrl.Result{}, err
	}
	switch {
	case reconcilerRes == resultStop:
		return ctrl.Result{}, nil
	case reconcilerRes == resultRequeue:
		return ctrl.Result{Requeue: true}, nil
	case reconcilerRes.resultRequeueAfter != nil:
		return ctrl.Result{RequeueAfter: *reconcilerRes.resultRequeueAfter}, nil
	}

	// TODO: Hackkyyyy
	// ONLY for DEMO
	NodeIP, err := getNodeIP(ctx, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.reportWorkSpaceStateReady(workspaceState, NodeIP, *svcNodePort)
	logger.Info(fmt.Sprintf("status: %#v", workspaceState.Status))
	return ctrl.Result{}, r.Client.Status().Update(ctx, workspaceState)
}

func (r *WorkspaceStateReconciler) ensureSVC(ctx context.Context, workspaceState *workspacev1alpha1.WorkspaceState) (subReconcilerResult, *int32, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconcile svc")
	desiredSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workspaceState.Name,
			Namespace: workspaceState.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeNodePort,
			Selector: StoragePodLabels(workspaceState),
			Ports: []corev1.ServicePort{
				{
					Name:       "tcp-port",
					Port:       873,
					TargetPort: intstr.FromInt(873),
				},
			},
		},
	}

	if err := controllerutil.SetOwnerReference(workspaceState, desiredSvc, r.Scheme); err != nil {
		return resultNil, nil, err
	}
	existingSvc := &corev1.Service{}

	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      desiredSvc.Name,
		Namespace: desiredSvc.Namespace},
		existingSvc,
	); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, nil, r.Create(ctx, desiredSvc)
		}
		return resultNil, nil, err
	}
	if !areServicesEqual(desiredSvc, existingSvc) {
		existingSvc.Spec = desiredSvc.Spec
		return resultRequeue, nil, r.Client.Update(ctx, existingSvc)
	}
	// TODO: check status conditions
	return resultNil, ptr.To(existingSvc.Spec.Ports[0].NodePort), nil
}

func (r *WorkspaceStateReconciler) reconcilePVCs(ctx context.Context, workspaceState *workspacev1alpha1.WorkspaceState) error {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconciling pvc")
	for i := range workspaceState.Spec.Resources {
		currentResource := &workspaceState.Spec.Resources[i]
		if err := r.ensurePVC(ctx, currentResource, workspaceState); err != nil {
			r.reportResourceReconcileError(err, workspaceState, currentResource)
			return err
		}
	}
	return nil
}

func (r *WorkspaceStateReconciler) ensurePVC(ctx context.Context, resource *workspacev1alpha1.WorkspaceResourceStorage, workspaceState *workspacev1alpha1.WorkspaceState) error {
	// TODO, change this based on the type.
	resourceSize, err := k8sresource.ParseQuantity(resource.Size)
	if err != nil {
		return fmt.Errorf("failed to parse resource size in the resource: %w", err)
	}

	desiredPVC := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resource.Name,
			Namespace: workspaceState.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resourceSize},
			},
			// TODO:
			// Hardcode to local path for now
			StorageClassName: stringPtr("local-path"),
		},
	}

	if err := controllerutil.SetOwnerReference(workspaceState, &desiredPVC, r.Scheme); err != nil {
		return fmt.Errorf("failed to set owner refs: %w", err)
	}

	existingPVC := &corev1.PersistentVolumeClaim{}

	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      desiredPVC.Name,
		Namespace: desiredPVC.Namespace,
	}, existingPVC); err != nil {
		if apierrors.IsNotFound(err) {
			return r.Client.Create(ctx, &desiredPVC)
		}
		return err
	}

	// TODO: check:
	// - desired object.spec == existing object.spec
	// - owner refs match
	// - PVC status to make sure they are ready.
	// - existingPVC.Status.Conditions to check if its ready, only proceed further object reconcilation
	// 	 if the pvc/storage is ready.
	return nil
}

func getNodeIP(ctx context.Context, client client.Client) (string, error) {
	nodeList := &corev1.NodeList{}
	err := client.List(ctx, nodeList)
	if err != nil {
		return "", fmt.Errorf("failed to list nodes: %v", err)
	}

	if len(nodeList.Items) == 0 {
		return "", fmt.Errorf("no nodes found")
	}

	// Get the IP address of the first node in the list
	node := nodeList.Items[0]
	nodeIP := ""

	// Iterate through the node's addresses and find the external IP
	for _, address := range node.Status.Addresses {
		if address.Type == corev1.NodeInternalIP {
			nodeIP = address.Address
			break
		}
	}

	if nodeIP == "" {
		return "", fmt.Errorf("no external IP found for the node")
	}

	return nodeIP, nil
}

func stringPtr(str string) *string {
	return &str
}

func (r *WorkspaceStateReconciler) reportResourceReconcileError(err error, workspaceState *v1alpha1.WorkspaceState, resource *v1alpha1.WorkspaceResourceStorage) {
	meta.SetStatusCondition(&workspaceState.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceStateConditionAvailable),
		Status:             metav1.ConditionFalse,
		Reason:             err.Error(),
		ObservedGeneration: workspaceState.Generation,
	})

	resourceInfo := FindResourceStatus(workspaceState.Status.WorkspaceStateInfo, resource)
	if resourceInfo == nil {
		workspaceState.Status.WorkspaceStateInfo = append(workspaceState.Status.WorkspaceStateInfo,
			v1alpha1.ResourceStateInfo{
				Name:              resource.Name,
				Status:            v1alpha1.ProvisionFailed,
				AddressIdentifier: "",
			},
		)
		return
	}

	resourceInfo.Status = v1alpha1.ProvisionFailed
}

func (r *WorkspaceStateReconciler) reportWorkSpaceStateReady(workspaceState *v1alpha1.WorkspaceState, nodeIP string, svcNodePort int32) {
	meta.SetStatusCondition(&workspaceState.Status.Conditions, metav1.Condition{
		Type:               string(v1alpha1.WorkspaceStateConditionAvailable),
		Status:             metav1.ConditionTrue,
		Reason:             "AllComponentsUP",
		ObservedGeneration: workspaceState.Generation,
	})

	res := make([]v1alpha1.ResourceStateInfo, 0)

	for _, resource := range workspaceState.Spec.Resources {
		res = append(res, v1alpha1.ResourceStateInfo{
			Name:              resource.Name,
			Status:            v1alpha1.Provisioned,
			AddressIdentifier: fmt.Sprintf("%s:%d/%s/", nodeIP, svcNodePort, resource.Name),
		})
	}
	workspaceState.Status.WorkspaceStateInfo = res
}

func FindResourceStatus(list []v1alpha1.ResourceStateInfo, resource *v1alpha1.WorkspaceResourceStorage) *v1alpha1.ResourceStateInfo {
	for i := range list {
		currentStatus := &list[i]
		if currentStatus.Name == resource.Name {
			return currentStatus
		}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *WorkspaceStateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&workspacev1alpha1.WorkspaceState{}).
		// We set controller ref if we want to work with .Owns()
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &workspacev1alpha1.WorkspaceState{})).
		Watches(&corev1.Service{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &workspacev1alpha1.WorkspaceState{})).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestForOwner(r.Scheme, mgr.GetRESTMapper(), &workspacev1alpha1.WorkspaceState{})).
		Complete(r)
}

func areServicesEqual(svc1, svc2 *corev1.Service) bool {
	// Create a copy of the services to avoid modifying the original objects
	svc1Copy := svc1.DeepCopy()
	svc2Copy := svc2.DeepCopy()

	// Iterate over the ports and set the nodePort to 0 for comparison
	for i := range svc1Copy.Spec.Ports {
		svc1Copy.Spec.Ports[i].NodePort = 0
	}
	for i := range svc2Copy.Spec.Ports {
		svc2Copy.Spec.Ports[i].NodePort = 0
	}

	// Use Semantic.DeepDerivative to compare the modified services
	return equality.Semantic.DeepDerivative(svc1Copy.Spec, svc2Copy.Spec)
}
