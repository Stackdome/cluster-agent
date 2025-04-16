package execute

import (
	"bytes"
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
)

type podExecuter struct {
	clientset *kubernetes.Clientset
	config    *rest.Config
}

func NewPodExecuter(config *rest.Config) (*podExecuter, error) {
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create clientset from config: %w", err)
	}

	return &podExecuter{
		clientset: clientset,
		config:    config,
	}, nil
}

func (p *podExecuter) ExecuteCommand(ctx context.Context, targetPod *corev1.Pod, containerName string, command []string) (string, error) {
	// If container name is empty, select the first container in the pod.
	if len(containerName) == 0 {
		containerName = targetPod.Spec.Containers[0].Name
	}
	namespace := targetPod.Namespace

	req := p.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(targetPod.Name).
		Namespace(namespace).
		SubResource("exec")

	var stdout, stderr bytes.Buffer
	execOptions := &corev1.PodExecOptions{
		Container: containerName,
		Command:   command,
		Stdout:    true,
		Stderr:    true,
	}

	req.VersionedParams(execOptions, scheme.ParameterCodec)

	executer, err := remotecommand.NewSPDYExecutor(p.config, "POST", req.URL())
	if err != nil {
		return "", fmt.Errorf("failed to create executor: %w", err)
	}

	streamOptions := remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
		Stdin:  nil,
	}

	streamErr := executer.StreamWithContext(ctx, streamOptions)
	if streamErr != nil {
		return "", fmt.Errorf("command execution failed: streamErr: %w, CommandErr: %s", streamErr, stderr.String())
	}

	return stdout.String(), nil
}
