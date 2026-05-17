package controller

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

type controllerContextKey int

const loggerContextKey controllerContextKey = iota

// Add a logger to the given context, to make it easier passing it around.
func ContextWithLogger(parent context.Context, logger logr.Logger) context.Context {
	return context.WithValue(parent, loggerContextKey, logger)
}

// Get the logger from the given context.
// Returning a discarding logger, if none was set.
func LoggerFromContext(ctx context.Context) logr.Logger {
	logger, ok := ctx.Value(loggerContextKey).(logr.Logger)
	if ok {
		return logger
	}
	return logr.Discard()
}

func DeploymentAvailable(deployment *appsv1.Deployment) bool {
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentAvailable &&
			condition.Status == corev1.ConditionTrue && deployment.Generation == deployment.Status.ObservedGeneration {
			return true
		}
	}
	return false
}

func DeploymentProgressDeadlineExceeded(deployment *appsv1.Deployment) bool {
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing &&
			condition.Status == corev1.ConditionFalse &&
			condition.Reason == "ProgressDeadlineExceeded" {
			return true
		}
	}
	return false
}

// DeploymentRolloutSettled returns true when the deployment rollout is no longer
// making progress — either it completed (NewReplicaSetAvailable) or timed out
// (ProgressDeadlineExceeded) — AND enough time has passed for pods to start and
// potentially crash. The grace period prevents stopping too early, before pods
// have had a chance to report failures.
func DeploymentRolloutSettled(deployment *appsv1.Deployment, gracePeriod time.Duration) bool {
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentProgressing {
			if condition.Status == corev1.ConditionFalse && condition.Reason == "ProgressDeadlineExceeded" {
				return true
			}
			if condition.Status == corev1.ConditionTrue && condition.Reason == "NewReplicaSetAvailable" {
				return time.Since(condition.LastTransitionTime.Time) >= gracePeriod
			}
		}
	}
	return false
}

func AreServicesEqual(svc1, svc2 *corev1.Service) bool {
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

func GetNodeIP(ctx context.Context, client client.Client) (string, error) {
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

func HasSameController(objA, objB metav1.Object) bool {
	controllerA := metav1.GetControllerOf(objA)
	controllerB := metav1.GetControllerOf(objB)
	if controllerA == nil || controllerB == nil {
		return false
	}
	return controllerA.UID == controllerB.UID
}

func GetNamespacedName(cr client.Object) types.NamespacedName {
	return types.NamespacedName{
		Namespace: cr.GetNamespace(),
		Name:      cr.GetName(),
	}
}

var crashReasons = map[string]bool{
	"CrashLoopBackOff":     true,
	"ImagePullBackOff":     true,
	"ErrImagePull":         true,
	"CreateContainerError": true,
	"OOMKilled":            true,
	"Error":                true,
}

func IsCrashState(cs corev1.ContainerStatus) bool {
	if cs.State.Waiting != nil && crashReasons[cs.State.Waiting.Reason] {
		return true
	}
	if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
		return true
	}
	return false
}

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func sanitizeTerminationMessage(msg string) string {
	msg = ansiEscapePattern.ReplaceAllString(msg, "")
	msg = strings.ReplaceAll(msg, "\r", "")
	return msg
}

func BuildLastFailureDetail(cs corev1.ContainerStatus) corev1alpha1.LastFailureDetail {
	detail := corev1alpha1.LastFailureDetail{
		ContainerName: cs.Name,
		RestartCount:  cs.RestartCount,
	}

	terminated := GetTerminatedState(cs)
	if terminated != nil {
		detail.LastTerminationExitCode = ptr.To(terminated.ExitCode)
		detail.LastTerminationReason = terminated.Reason
		detail.LastTerminationMessage = sanitizeTerminationMessage(terminated.Message)
	} else if cs.State.Waiting != nil {
		detail.LastTerminationReason = cs.State.Waiting.Reason
		detail.LastTerminationMessage = sanitizeTerminationMessage(cs.State.Waiting.Message)
	}

	return detail
}

func GetTerminatedState(cs corev1.ContainerStatus) *corev1.ContainerStateTerminated {
	if cs.State.Terminated != nil {
		return cs.State.Terminated
	}
	if cs.LastTerminationState.Terminated != nil {
		return cs.LastTerminationState.Terminated
	}
	return nil
}

type Named interface {
	GetName() string
}

func Unique[T Named](items []T) []T {
	index := make(map[string]struct{})
	list := []T{}
	for _, item := range items {
		if _, found := index[item.GetName()]; !found {
			index[item.GetName()] = struct{}{}
			list = append(list, item)
		}
	}
	return list
}
