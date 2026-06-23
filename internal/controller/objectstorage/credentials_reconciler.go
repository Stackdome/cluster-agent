package objectstorage

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
)

type credentialsReconciler struct {
	client client.Client
	scheme *runtime.Scheme
}

func newCredentialsReconciler(c client.Client, scheme *runtime.Scheme) *credentialsReconciler {
	return &credentialsReconciler{client: c, scheme: scheme}
}

func (r *credentialsReconciler) name() string { return "credentials-reconciler" }

func (r *credentialsReconciler) reconcile(ctx context.Context, resource *storagev1alpha1.ObjectStorage) (subReconcilerResult, error) {
	logger := log.FromContext(ctx)

	existingSecret := &corev1.Secret{}
	if err := r.client.Get(ctx, client.ObjectKey{
		Name:      resource.CredentialsSecretName(),
		Namespace: resource.Namespace,
	}, existingSecret); err != nil {
		if !apierrors.IsNotFound(err) {
			return resultNil, err
		}

		accessKey, err := generateRandomKey(20)
		if err != nil {
			return resultNil, err
		}
		secretKey, err := generateRandomKey(40)
		if err != nil {
			return resultNil, err
		}

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      resource.CredentialsSecretName(),
				Namespace: resource.Namespace,
			},
			Data: map[string][]byte{
				storagev1alpha1.ObjectStorageSecretKeyAccessKey:    []byte(accessKey),
				storagev1alpha1.ObjectStorageSecretKeySecretKey:    []byte(secretKey),
				storagev1alpha1.ObjectStorageSecretKeyAWSAccessKey: []byte(accessKey),
				storagev1alpha1.ObjectStorageSecretKeyAWSSecretKey: []byte(secretKey),
			},
		}

		if err := controllerutil.SetControllerReference(resource, secret, r.scheme); err != nil {
			return resultNil, err
		}

		logger.Info("Creating credentials Secret", "name", secret.Name)
		return resultRequeue, r.client.Create(ctx, secret)
	}

	resource.Status.CredentialsSecretName = existingSecret.Name
	return resultNil, nil
}

func generateRandomKey(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes)[:length], nil
}
