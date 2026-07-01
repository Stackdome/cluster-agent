package fixtures

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

const defaultTestImage = "nginx:1.25-alpine"

// ResourceOption customises a StackResource produced by NewResource.
type ResourceOption func(*corev1alpha1.StackResource)

// NewStack builds a StackWithResources from pre-built StackResource objects.
// The Stack's ResourceNames field is derived from the resource names automatically.
func NewStack(name string, resources ...*corev1alpha1.StackResource) *StackWithResources {
	names := make([]string, len(resources))
	for i, r := range resources {
		names[i] = r.Name
	}
	return &StackWithResources{
		Stack: &corev1alpha1.Stack{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: defaultNamespace,
				Labels:    stackLabels(name),
			},
			Spec: corev1alpha1.StackSpec{ResourceNames: names},
		},
		Resources: resources,
	}
}

// NewResource builds a StackResource with sensible defaults (nginx image, one
// HTTP port) and applies any supplied options on top.
func NewResource(stackName, resourceName string, opts ...ResourceOption) *corev1alpha1.StackResource {
	sr := &corev1alpha1.StackResource{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: defaultNamespace,
			Labels:    resourceLabels(stackName, resourceName),
		},
		Spec: corev1alpha1.StackResourceSpec{
			ImageSpec: &corev1alpha1.ImageSpec{Image: defaultTestImage},
			Ports: []corev1alpha1.Port{
				{Name: "http", Number: 80, Protocol: "http", FQDN: resourceName + ".local"},
			},
		},
	}
	for _, opt := range opts {
		opt(sr)
	}
	return sr
}

// ---------------------------------------------------------------------------
// Resource options
// ---------------------------------------------------------------------------

// WithReplicas sets the replica count for the resource.
func WithReplicas(n int32) ResourceOption {
	return func(sr *corev1alpha1.StackResource) { sr.Spec.Replicas = ptr.To(n) }
}

// WithImage overrides the default container image.
func WithImage(image string) ResourceOption {
	return func(sr *corev1alpha1.StackResource) {
		sr.Spec.ImageSpec = &corev1alpha1.ImageSpec{Image: image}
	}
}

// WithPorts replaces the default port list.
func WithPorts(ports ...corev1alpha1.Port) ResourceOption {
	return func(sr *corev1alpha1.StackResource) { sr.Spec.Ports = ports }
}

// WithoutPorts removes all ports from the resource.
func WithoutPorts() ResourceOption {
	return func(sr *corev1alpha1.StackResource) { sr.Spec.Ports = nil }
}

// WithEnv sets environment variables on the resource.
func WithEnv(vars ...corev1alpha1.EnvironmentVariable) ResourceOption {
	return func(sr *corev1alpha1.StackResource) { sr.Spec.EnvironmentVariables = vars }
}

// WithDependsOn sets the dependency list for the resource.
func WithDependsOn(names ...string) ResourceOption {
	return func(sr *corev1alpha1.StackResource) { sr.Spec.DependsOn = names }
}

// WithCommand sets both Command and Args on the resource.
func WithCommand(command, args []string) ResourceOption {
	return func(sr *corev1alpha1.StackResource) {
		sr.Spec.Command = command
		sr.Spec.Args = args
	}
}

// WithInit sets an init container spec on the resource.
func WithInit(init *corev1alpha1.InitSpec) ResourceOption {
	return func(sr *corev1alpha1.StackResource) { sr.Spec.Init = init }
}

// WithWorkloadType sets the workload type (Service, Worker, etc.).
func WithWorkloadType(t corev1alpha1.WorkloadType) ResourceOption {
	return func(sr *corev1alpha1.StackResource) { sr.Spec.WorkloadType = t }
}

// WithHealthChecks sets readiness/liveness/startup probes.
func WithHealthChecks(hc *corev1alpha1.HealthChecks) ResourceOption {
	return func(sr *corev1alpha1.StackResource) { sr.Spec.HealthChecks = hc }
}

// WithSchedule sets the cron schedule for CronJob workloads.
func WithSchedule(schedule string) ResourceOption {
	return func(sr *corev1alpha1.StackResource) { sr.Spec.Schedule = schedule }
}

// WithHardenedSecurity enables hardened security defaults on the resource.
func WithHardenedSecurity() ResourceOption {
	return func(sr *corev1alpha1.StackResource) { sr.Spec.HardenedSecurityDefaults = ptr.To(true) }
}

// WithBuildSpec replaces the ImageSpec with a BuildSpec.
func WithBuildSpec(bs *corev1alpha1.StackResourceBuildSpec) ResourceOption {
	return func(sr *corev1alpha1.StackResource) {
		sr.Spec.ImageSpec = nil
		sr.Spec.BuildSpec = bs
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// OwnerRefTo returns a controller owner reference pointing at the given Stack.
func OwnerRefTo(stack *corev1alpha1.Stack) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         "core.stackdome.io/v1alpha1",
		Kind:               "Stack",
		Name:               stack.Name,
		UID:                stack.UID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
}

// CreateResourceForStack stamps a revision annotation and owner reference on
// the resource, then creates it via the API server.
func CreateResourceForStack(ctx context.Context, c client.Client, stack *corev1alpha1.Stack, sr *corev1alpha1.StackResource) error {
	setRevisionAnnotation(sr)
	sr.OwnerReferences = []metav1.OwnerReference{OwnerRefTo(stack)}
	return c.Create(ctx, sr)
}
