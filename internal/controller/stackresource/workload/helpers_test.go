package workload

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

func TestBuildEnvVarsLiteralAndValueFrom(t *testing.T) {
	resource := &v1alpha1.StackResource{Spec: v1alpha1.StackResourceSpec{
		EnvironmentVariables: []v1alpha1.EnvironmentVariable{
			{Name: "PLAIN", Value: "v"},
			{Name: "SECRET", ValueFrom: &v1alpha1.EnvVarSource{
				SecretKeyRef: corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: "s"},
					Key:                  "k",
				},
			}},
		},
	}}

	envs := buildEnvVars(resource)

	if len(envs) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(envs))
	}
	if envs[0].Value != "v" || envs[0].ValueFrom != nil {
		t.Fatalf("literal env mapped wrong: %+v", envs[0])
	}
	if envs[1].ValueFrom == nil || envs[1].ValueFrom.SecretKeyRef == nil ||
		envs[1].ValueFrom.SecretKeyRef.Name != "s" || envs[1].ValueFrom.SecretKeyRef.Key != "k" {
		t.Fatalf("valueFrom env mapped wrong: %+v", envs[1])
	}
}

func TestBuildProbeResolvesNamedPort(t *testing.T) {
	ports := []v1alpha1.Port{{Name: "http", Number: 8080}}
	probe, err := buildProbe(&v1alpha1.Probe{
		HTTPGet: &v1alpha1.HTTPGetProbe{Path: "/health", PortName: "http"},
	}, ports)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if probe.HTTPGet.Port.IntValue() != 8080 {
		t.Fatalf("expected port 8080, got %d", probe.HTTPGet.Port.IntValue())
	}
}

func TestBuildProbeUnknownPortNameFails(t *testing.T) {
	_, err := buildProbe(&v1alpha1.Probe{
		HTTPGet: &v1alpha1.HTTPGetProbe{PortName: "nope"},
	}, []v1alpha1.Port{{Name: "http", Number: 8080}})

	if err == nil {
		t.Fatal("expected error for unknown port name, got nil")
	}
}

func TestBuildProbeNilReturnsNil(t *testing.T) {
	probe, err := buildProbe(nil, nil)
	if err != nil || probe != nil {
		t.Fatalf("nil probe must be (nil, nil), got (%v, %v)", probe, err)
	}
}

func TestApplySecurityDefaultsIsOptIn(t *testing.T) {
	base := func() *corev1.PodSpec {
		return &corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}}
	}

	for _, hardened := range []*bool{nil, ptr.To(false)} {
		spec := base()
		applySecurityDefaults(spec, hardened)
		if spec.SecurityContext != nil || spec.Containers[0].SecurityContext != nil {
			t.Fatalf("hardened=%v must leave pod spec untouched", hardened)
		}
	}

	spec := base()
	applySecurityDefaults(spec, ptr.To(true))
	if spec.SecurityContext == nil || spec.SecurityContext.RunAsNonRoot == nil || !*spec.SecurityContext.RunAsNonRoot {
		t.Fatal("hardened=true must set runAsNonRoot")
	}
	if spec.Containers[0].SecurityContext == nil ||
		spec.Containers[0].SecurityContext.AllowPrivilegeEscalation == nil ||
		*spec.Containers[0].SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("hardened=true must disable privilege escalation on containers")
	}
}

func TestPortNumberByName(t *testing.T) {
	ports := []v1alpha1.Port{
		{Name: "http", Number: 8080},
		{Name: "grpc", Number: 9090},
	}

	n, ok := portNumberByName("http", ports)
	if !ok || n != 8080 {
		t.Fatalf("expected (8080, true), got (%d, %v)", n, ok)
	}

	n, ok = portNumberByName("grpc", ports)
	if !ok || n != 9090 {
		t.Fatalf("expected (9090, true), got (%d, %v)", n, ok)
	}

	_, ok = portNumberByName("nope", ports)
	if ok {
		t.Fatal("expected (_, false) for unknown port")
	}
}
