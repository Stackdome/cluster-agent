package helpers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	"sigs.k8s.io/controller-runtime/pkg/client"

	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
)

func WaitForClusterRegistryReady(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) (*registryv1alpha1.ClusterRegistry, error) {
	return WaitFor(ctx, c, key, &registryv1alpha1.ClusterRegistry{}, func(reg *registryv1alpha1.ClusterRegistry) bool {
		return reg.Status.Phase == registryv1alpha1.RegistryPhaseRunning
	}, timeout)
}

func WaitForClusterRegistryDeleted(ctx context.Context, c client.Client, key client.ObjectKey, timeout time.Duration) error {
	return WaitForDeleted(ctx, c, key, &registryv1alpha1.ClusterRegistry{}, timeout)
}

func ClusterRegistryHasCondition(reg *registryv1alpha1.ClusterRegistry, conditionType string, status metav1.ConditionStatus) bool {
	cond := meta.FindStatusCondition(reg.Status.Conditions, conditionType)
	return cond != nil && cond.Status == status
}

// ImageTagPresent reports whether the given "<repository>:<tag>" exists in the
// in-cluster registry. Since the registry is a ClusterIP service, it opens a
// short-lived port-forward to the registry pod and issues a manifest HEAD via
// go-containerregistry (basic auth over plain HTTP, matching the registry's
// htpasswd config). A 404 means "absent" (not an error), so this is safe to poll.
func ImageTagPresent(
	ctx context.Context,
	restConfig *rest.Config,
	kubeClient kubernetes.Interface,
	registryNamespace, registryName string,
	remotePort int,
	repoAndTag, username, password string,
) (bool, error) {
	podName, err := registryPodName(ctx, kubeClient, registryNamespace, registryName)
	if err != nil {
		return false, err
	}
	localAddr, stop, err := portForwardPod(restConfig, registryNamespace, podName, remotePort)
	if err != nil {
		return false, err
	}
	defer stop()

	ref, err := name.NewTag(fmt.Sprintf("%s/%s", localAddr, repoAndTag), name.Insecure)
	if err != nil {
		return false, err
	}
	opts := []remote.Option{remote.WithContext(ctx)}
	if username != "" {
		opts = append(opts, remote.WithAuth(&authn.Basic{Username: username, Password: password}))
	}
	if _, err := remote.Head(ref, opts...); err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, fmt.Errorf("HEAD %s: %w", repoAndTag, err)
	}
	return true, nil
}

func registryPodName(ctx context.Context, kubeClient kubernetes.Interface, namespace, registryName string) (string, error) {
	pods, err := kubeClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + registryName,
	})
	if err != nil {
		return "", err
	}
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodRunning {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("no running pod found for registry %q in namespace %q", registryName, namespace)
}

// portForwardPod forwards a random local port to remotePort on the pod and
// returns the local "127.0.0.1:<port>" address plus a stop function.
func portForwardPod(restConfig *rest.Config, namespace, podName string, remotePort int) (string, func(), error) {
	roundTripper, upgrader, err := spdy.RoundTripperFor(restConfig)
	if err != nil {
		return "", nil, err
	}
	hostURL, err := url.Parse(restConfig.Host)
	if err != nil {
		return "", nil, err
	}
	serverURL := &url.URL{
		Scheme: "https",
		Host:   hostURL.Host,
		Path:   fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/portforward", namespace, podName),
	}
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, http.MethodPost, serverURL)

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	fw, err := portforward.NewOnAddresses(
		dialer, []string{"127.0.0.1"}, []string{fmt.Sprintf("0:%d", remotePort)},
		stopCh, readyCh, io.Discard, io.Discard,
	)
	if err != nil {
		return "", nil, err
	}

	errCh := make(chan error, 1)
	go func() { errCh <- fw.ForwardPorts() }()

	select {
	case <-readyCh:
	case err := <-errCh:
		return "", nil, fmt.Errorf("port-forward to %s failed: %w", podName, err)
	case <-time.After(20 * time.Second):
		close(stopCh)
		return "", nil, fmt.Errorf("port-forward to %s not ready in time", podName)
	}

	ports, err := fw.GetPorts()
	if err != nil {
		close(stopCh)
		return "", nil, err
	}
	return fmt.Sprintf("127.0.0.1:%d", ports[0].Local), func() { close(stopCh) }, nil
}
