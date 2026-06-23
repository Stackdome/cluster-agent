package helpers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storagev1alpha1 "stackdome.io/cluster-agent/api/storage/v1alpha1"
)

// WaitForObjectStorageReady polls until the ObjectStorage reaches Available=True
// or the timeout expires.
func WaitForObjectStorageReady(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) (*storagev1alpha1.ObjectStorage, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			os := &storagev1alpha1.ObjectStorage{}
			_ = c.Get(ctx, key, os)
			return os, fmt.Errorf("timed out waiting for ObjectStorage %s to become Available (current phase: %s)", key.Name, os.Status.Phase)
		case <-tick.C:
			os := &storagev1alpha1.ObjectStorage{}
			if err := c.Get(ctx, key, os); err != nil {
				continue
			}
			cond := meta.FindStatusCondition(os.Status.Conditions, storagev1alpha1.ObjectStorageConditionAvailable)
			if cond != nil && cond.Status == metav1.ConditionTrue && cond.ObservedGeneration == os.Generation {
				return os, nil
			}
		}
	}
}

// WaitForCondition polls until a specific condition is set on the ObjectStorage.
func WaitForObjectStorageCondition(ctx context.Context, c client.Client, key client.ObjectKey, conditionType string, expectedStatus metav1.ConditionStatus, timeout time.Duration) (*storagev1alpha1.ObjectStorage, error) {
	deadline := time.After(timeout)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			os := &storagev1alpha1.ObjectStorage{}
			_ = c.Get(ctx, key, os)
			return os, fmt.Errorf("timed out waiting for condition %s=%s on ObjectStorage %s", conditionType, expectedStatus, key.Name)
		case <-tick.C:
			os := &storagev1alpha1.ObjectStorage{}
			if err := c.Get(ctx, key, os); err != nil {
				continue
			}
			cond := meta.FindStatusCondition(os.Status.Conditions, conditionType)
			if cond != nil && cond.Status == expectedStatus {
				return os, nil
			}
		}
	}
}

// WaitForObjectStorageDeleted polls until the ObjectStorage no longer exists.
func WaitForObjectStorageDeleted(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) error {
	deadline := time.After(timeout)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	for {
		select {
		case <-deadline:
			return fmt.Errorf("timed out waiting for ObjectStorage %s to be deleted", key.Name)
		case <-tick.C:
			os := &storagev1alpha1.ObjectStorage{}
			if err := c.Get(ctx, key, os); err != nil {
				if errors.IsNotFound(err) {
					return nil
				}
			}
		}
	}
}

// HasCondition checks if an ObjectStorage has a specific condition with the expected status.
func HasObjectStorageCondition(os *storagev1alpha1.ObjectStorage, conditionType string, status metav1.ConditionStatus) bool {
	cond := meta.FindStatusCondition(os.Status.Conditions, conditionType)
	return cond != nil && cond.Status == status
}

// GetVolumeForObjectStorage retrieves the Volume CR created for an ObjectStorage.
func GetVolumeForObjectStorage(ctx context.Context, c client.Client, namespace, objectStorageName string) (*storagev1alpha1.Volume, error) {
	os := &storagev1alpha1.ObjectStorage{}
	if err := c.Get(ctx, client.ObjectKey{Name: objectStorageName, Namespace: namespace}, os); err != nil {
		return nil, err
	}

	vol := &storagev1alpha1.Volume{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      os.VolumeName(),
		Namespace: namespace,
	}, vol)
	return vol, err
}

// GetCredentialsSecretForObjectStorage retrieves the credentials Secret created for an ObjectStorage.
func GetCredentialsSecretForObjectStorage(ctx context.Context, c client.Client, namespace, objectStorageName string) (*corev1.Secret, error) {
	os := &storagev1alpha1.ObjectStorage{}
	if err := c.Get(ctx, client.ObjectKey{Name: objectStorageName, Namespace: namespace}, os); err != nil {
		return nil, err
	}

	secret := &corev1.Secret{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      os.CredentialsSecretName(),
		Namespace: namespace,
	}, secret)
	return secret, err
}

// GetDeploymentForObjectStorage retrieves the Deployment created for an ObjectStorage.
func GetDeploymentForObjectStorage(ctx context.Context, c client.Client, namespace, objectStorageName string) (*appsv1.Deployment, error) {
	os := &storagev1alpha1.ObjectStorage{}
	if err := c.Get(ctx, client.ObjectKey{Name: objectStorageName, Namespace: namespace}, os); err != nil {
		return nil, err
	}

	dep := &appsv1.Deployment{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      os.DeploymentName(),
		Namespace: namespace,
	}, dep)
	return dep, err
}

// GetServiceForObjectStorage retrieves the Service created for an ObjectStorage.
func GetServiceForObjectStorage(ctx context.Context, c client.Client, namespace, objectStorageName string) (*corev1.Service, error) {
	os := &storagev1alpha1.ObjectStorage{}
	if err := c.Get(ctx, client.ObjectKey{Name: objectStorageName, Namespace: namespace}, os); err != nil {
		return nil, err
	}

	svc := &corev1.Service{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      os.ServiceName(),
		Namespace: namespace,
	}, svc)
	return svc, err
}

// GetIngressForObjectStorage retrieves the Ingress created for an ObjectStorage.
func GetIngressForObjectStorage(ctx context.Context, c client.Client, namespace, objectStorageName string) (*networkingv1.Ingress, error) {
	os := &storagev1alpha1.ObjectStorage{}
	if err := c.Get(ctx, client.ObjectKey{Name: objectStorageName, Namespace: namespace}, os); err != nil {
		return nil, err
	}

	ingress := &networkingv1.Ingress{}
	err := c.Get(ctx, client.ObjectKey{
		Name:      os.IngressName(),
		Namespace: namespace,
	}, ingress)
	return ingress, err
}

// GetPodForDeployment finds a Running pod owned by the given deployment.
func GetPodForDeployment(ctx context.Context, kubeClient kubernetes.Interface, namespace, deploymentName string) (string, error) {
	selector := labels.Set{"app": deploymentName}.AsSelector()
	pods, err := kubeClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return "", fmt.Errorf("listing pods for deployment %s: %w", deploymentName, err)
	}
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			return pod.Name, nil
		}
	}
	return "", fmt.Errorf("no running pod found for deployment %s", deploymentName)
}

// PortForwardToPod sets up port-forwarding to a pod and returns the local port and a stop channel.
func PortForwardToPod(cfg *rest.Config, namespace, podName string, remotePort int) (uint16, chan struct{}, error) {
	roundTripper, upgrader, err := spdy.RoundTripperFor(cfg)
	if err != nil {
		return 0, nil, fmt.Errorf("creating round tripper: %w", err)
	}

	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, podName)
	hostURL, err := url.Parse(cfg.Host)
	if err != nil {
		return 0, nil, fmt.Errorf("parsing host URL: %w", err)
	}
	hostURL.Path = path

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, hostURL)

	stopChan := make(chan struct{}, 1)
	readyChan := make(chan struct{})

	ports := []string{fmt.Sprintf("0:%d", remotePort)}
	fw, err := portforward.New(dialer, ports, stopChan, readyChan, nil, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("creating port forwarder: %w", err)
	}

	errChan := make(chan error, 1)
	go func() {
		errChan <- fw.ForwardPorts()
	}()

	select {
	case <-readyChan:
	case err := <-errChan:
		return 0, nil, fmt.Errorf("port forward failed: %w", err)
	}

	forwardedPorts, err := fw.GetPorts()
	if err != nil {
		close(stopChan)
		return 0, nil, fmt.Errorf("getting forwarded ports: %w", err)
	}

	return forwardedPorts[0].Local, stopChan, nil
}
