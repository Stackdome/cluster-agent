package workspacestorage

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	workspacev1alpha1 "soradev.io/cluster-agent/api/v1alpha1"
	"soradev.io/cluster-agent/internal/controller"
)

const (
	SSH_SECRET_KEY = "authorized_keys"
)

// Exposes the storage server to the outside world for syncing.
type userSShKeySecretReconciler struct {
	client.Client
	UncachedClient client.Client
	Scheme         *runtime.Scheme
}

func StorageServerSSHSecretName(workspaceStorage *workspacev1alpha1.WorkspaceStorage) string {
	return fmt.Sprintf("%s-public-ssh-secret", workspaceStorage.Name)
}

func (r *userSShKeySecretReconciler) reconcile(ctx context.Context, workspaceStorage *workspacev1alpha1.WorkspaceStorage) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconcile user public ssh key secret")
	desiredSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      StorageServerSSHSecretName(workspaceStorage),
			Namespace: workspaceStorage.Namespace,
			Labels:    WorkspaceStorageLabels(workspaceStorage),
		},
		Data: map[string][]byte{
			SSH_SECRET_KEY: []byte(workspaceStorage.Spec.UserPublicSSHKey),
		},
	}

	if err := controllerutil.SetControllerReference(workspaceStorage, desiredSecret, r.Scheme); err != nil {
		return resultNil, err
	}
	existingSecret := &corev1.Secret{}
	if err := r.UncachedClient.Get(ctx, controller.GetNamespacedName(desiredSecret), existingSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeue, r.Create(ctx, desiredSecret)
		}
		return resultNil, err
	}
	existingData := existingSecret.StringData
	existingSSHkey, found := existingData[SSH_SECRET_KEY]
	if !found || existingSSHkey != workspaceStorage.Spec.UserPublicSSHKey {
		existingSecret.Data = desiredSecret.Data
		return resultRequeue, r.Update(ctx, existingSecret)
	}

	return resultNil, nil
}
