package stackstorage

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"

	"stackdome.io/cluster-agent/internal/controller"
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

func StorageServerSSHSecretName(workspaceStorage *storagev1alpha1.Storage) string {
	return fmt.Sprintf("%s-public-ssh-secret", workspaceStorage.Name)
}

func (r *userSShKeySecretReconciler) reconcile(ctx context.Context, storage *storagev1alpha1.Storage) (subReconcilerResult, error) {
	logger := controller.LoggerFromContext(ctx)
	logger.Info("reconcile user public ssh key secret")
	// decodedPublicKey, err := base64.StdEncoding.DecodeString(workspaceStorage.Spec.UserPublicSSHKey)
	// if err != nil {
	// 	return resultNil, fmt.Errorf("failed to decode base64 encoded public key: %w", err)
	// }
	desiredSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      StorageServerSSHSecretName(storage),
			Namespace: storage.Namespace,
			Labels:    storageLabels(storage),
		},
		Data: map[string][]byte{
			SSH_SECRET_KEY: []byte(storage.Spec.UserPublicSSHKey),
		},
	}

	if err := controllerutil.SetControllerReference(storage, desiredSecret, r.Scheme); err != nil {
		return resultNil, err
	}
	existingSecret := &corev1.Secret{}
	if err := r.UncachedClient.Get(ctx, controller.GetNamespacedName(desiredSecret), existingSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return resultRequeueAfter(time.Second), r.Create(ctx, desiredSecret)
		}
		return resultNil, err
	}
	existingData, found := existingSecret.Data[SSH_SECRET_KEY]
	if !found || string(existingData) != storage.Spec.UserPublicSSHKey {
		logger := controller.LoggerFromContext(ctx)
		logger.Info("updating secret existing secret")
		existingSecret.Data = desiredSecret.Data
		return resultRequeueAfter(time.Second), r.Update(ctx, existingSecret)
	}

	return resultNil, nil
}
