package zotregistry

import (
	"context"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
	"stackdome.io/cluster-agent/pkg/registry"
)

func newTestBuilder(t *testing.T) registry.RegistryBuilder {
	t.Helper()
	scheme := k8sruntime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	builder := NewZotRegistry(ZotRegistryOpts{
		RegistryImage:                 "ghcr.io/project-zot/zot:latest",
		RegistryConfigReconcilerImage: "test/reconciler:latest",
		EnableGC:                      true,
		GCDelay:                       "1h",
		GCInterval:                    "24h",
		RegistryLogLevel:              "info",
	})
	err := builder.Initialize(registry.RegistryBuilderOpts{
		Client:           fakeClient,
		StorageDirectory: "/var/lib/registry",
		ConfigPath:       "/etc/registry/config.json",
		Auth: registry.AuthOpts{
			Htpasswd: registry.HtpasswdOpts{Path: "/etc/auth/htpasswd"},
		},
		Namespace: "stackdome-registry",
	})
	if err != nil {
		t.Fatalf("failed to initialize builder: %v", err)
	}
	return builder
}

func newTestRegistry(name string, port int32) *registryv1alpha1.ClusterRegistry {
	return &registryv1alpha1.ClusterRegistry{
		ObjectMeta: v1.ObjectMeta{Name: name},
		Spec: registryv1alpha1.ClusterRegistrySpec{
			Owner:   registryv1alpha1.RegistryOwner{Type: "Organization", ID: "test"},
			Storage: registryv1alpha1.RegistryStorageSpec{Size: "10Gi"},
			Port:    port,
			Auth: &registryv1alpha1.RegistryAuthSpec{
				HtPasswordCredentials: &registryv1alpha1.HtPasswordCredentialsSpec{
					CredentialsRef: &corev1alpha1.CredentialSecretKeyPair{
						SecretRef:   corev1.SecretReference{Name: "creds", Namespace: "stackdome-registry"},
						UsernameKey: "username",
						PasswordKey: "password",
					},
				},
			},
		},
	}
}

func TestBuildRegistryConfigReconcilerDaemonset_Security(t *testing.T) {
	builder := newTestBuilder(t)
	reg := newTestRegistry("test-reg", 5000)
	ds := builder.BuildRegistryConfigReconcilerDaemonset(context.Background(), reg, "test-cm", "registries.json", registry.RuntimeContainerd)

	podSpec := ds.Spec.Template.Spec
	if !podSpec.HostPID {
		t.Error("DaemonSet must have HostPID=true to find and SIGHUP containerd on the host")
	}
	if podSpec.SecurityContext == nil {
		t.Fatal("pod SecurityContext should not be nil")
	}
	if podSpec.SecurityContext.RunAsUser == nil || *podSpec.SecurityContext.RunAsUser != 0 {
		t.Errorf("pod SecurityContext.RunAsUser should be 0 (root required to write host containerd config), got %v", podSpec.SecurityContext.RunAsUser)
	}

	container := podSpec.Containers[0]
	if container.SecurityContext == nil {
		t.Fatal("container SecurityContext should not be nil")
	}
	if container.SecurityContext.AllowPrivilegeEscalation == nil || *container.SecurityContext.AllowPrivilegeEscalation {
		t.Error("container AllowPrivilegeEscalation should be false")
	}
	if container.SecurityContext.ReadOnlyRootFilesystem == nil || !*container.SecurityContext.ReadOnlyRootFilesystem {
		t.Error("container ReadOnlyRootFilesystem should be true")
	}
	if container.SecurityContext.Capabilities == nil || len(container.SecurityContext.Capabilities.Drop) == 0 {
		t.Fatal("container should drop capabilities")
	}
	if container.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Errorf("container should drop ALL capabilities, got %v", container.SecurityContext.Capabilities.Drop)
	}
}

func TestBuildRegistryConfigReconcilerDaemonset_Labels(t *testing.T) {
	builder := newTestBuilder(t)
	reg := newTestRegistry("test-reg", 5000)
	ds := builder.BuildRegistryConfigReconcilerDaemonset(context.Background(), reg, "test-cm", "registries.json", registry.RuntimeContainerd)

	selectorLabels := ds.Spec.Selector.MatchLabels
	if _, ok := selectorLabels["demonset-for"]; ok {
		t.Error("label key should be 'daemonset-for', not 'demonset-for' (typo)")
	}
	if val, ok := selectorLabels["daemonset-for"]; !ok || val != "registry-config-reconciler" {
		t.Errorf("selector label 'daemonset-for' should be 'registry-config-reconciler', got %q", val)
	}

	templateLabels := ds.Spec.Template.Labels
	if val, ok := templateLabels["daemonset-for"]; !ok || val != "registry-config-reconciler" {
		t.Errorf("template label 'daemonset-for' should be 'registry-config-reconciler', got %q", val)
	}
}

func TestBuildRegistryConfigReconcilerDaemonset_Name(t *testing.T) {
	builder := newTestBuilder(t)
	reg := newTestRegistry("test-reg", 5000)
	ds := builder.BuildRegistryConfigReconcilerDaemonset(context.Background(), reg, "test-cm", "registries.json", registry.RuntimeContainerd)

	if ds.Name != registry.RegistryConfigReconcilerDaemonSetName {
		t.Errorf("DaemonSet name should be %q, got %q", registry.RegistryConfigReconcilerDaemonSetName, ds.Name)
	}
}

func TestBuildHTPasswordSecret(t *testing.T) {
	builder := newTestBuilder(t)
	reg := newTestRegistry("test-reg", 5000)

	secret, key, err := builder.BuildHTPasswordSecret(context.Background(), reg, "admin", "s3cret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != HtpasswordKey {
		t.Errorf("key should be %q, got %q", HtpasswordKey, key)
	}

	entry := string(secret.Data[HtpasswordKey])
	parts := strings.SplitN(entry, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("entry should be 'username:hash', got %q", entry)
	}
	if parts[0] != "admin" {
		t.Errorf("username should be 'admin', got %q", parts[0])
	}

	cost, err := bcrypt.Cost([]byte(parts[1]))
	if err != nil {
		t.Fatalf("failed to get bcrypt cost: %v", err)
	}
	if cost != bcrypt.DefaultCost {
		t.Errorf("bcrypt cost should be %d (DefaultCost), got %d", bcrypt.DefaultCost, cost)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(parts[1]), []byte("s3cret")); err != nil {
		t.Error("bcrypt hash should validate against original password")
	}
}

func TestBuildService(t *testing.T) {
	builder := newTestBuilder(t)
	reg := newTestRegistry("test-reg", 5000)

	svc, url, err := builder.BuildService(context.Background(), reg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(svc.Spec.Ports) == 0 {
		t.Fatal("service should have at least one port")
	}
	port := svc.Spec.Ports[0]
	if port.Port != 80 {
		t.Errorf("service port should be 80, got %d", port.Port)
	}
	if port.TargetPort.IntVal != 5000 {
		t.Errorf("target port should be 5000, got %d", port.TargetPort.IntVal)
	}

	if !strings.Contains(url, "test-reg") {
		t.Errorf("URL should contain registry name, got %q", url)
	}
	if !strings.Contains(url, "stackdome-registry") {
		t.Errorf("URL should contain namespace, got %q", url)
	}
}

func TestBuildRegistryConfigReconcilerDaemonset_ContainerdRuntime(t *testing.T) {
	builder := newTestBuilder(t)
	reg := newTestRegistry("test-reg", 5000)
	ds := builder.BuildRegistryConfigReconcilerDaemonset(context.Background(), reg, "test-cm", "registries.json", registry.RuntimeContainerd)

	podSpec := ds.Spec.Template.Spec

	var hostPathVolume *corev1.HostPathVolumeSource
	for _, v := range podSpec.Volumes {
		if v.HostPath != nil {
			hostPathVolume = v.HostPath
			break
		}
	}
	if hostPathVolume == nil {
		t.Fatal("expected a hostPath volume")
	}
	if hostPathVolume.Path != "/etc/containerd" {
		t.Errorf("hostPath should be /etc/containerd for containerd runtime, got %s", hostPathVolume.Path)
	}
	if hostPathVolume.Type == nil || *hostPathVolume.Type != corev1.HostPathDirectory {
		t.Errorf("hostPath type should be Directory for containerd runtime, got %v", hostPathVolume.Type)
	}

	container := podSpec.Containers[0]
	argsStr := strings.Join(container.Args, " ")
	if !strings.Contains(argsStr, "--runtime=containerd") {
		t.Errorf("container args should include --runtime=containerd, got %v", container.Args)
	}
	if !strings.Contains(argsStr, "--config-dir=/etc/containerd") {
		t.Errorf("container args should include --config-dir=/etc/containerd, got %v", container.Args)
	}
	if !strings.Contains(argsStr, "--config-file=config.toml") {
		t.Error("container args should include --config-file=config.toml for containerd runtime")
	}

	var configMount *corev1.VolumeMount
	for i, vm := range container.VolumeMounts {
		if vm.Name == "containerd-config" {
			configMount = &container.VolumeMounts[i]
			break
		}
	}
	if configMount == nil {
		t.Fatal("expected containerd-config volume mount")
	}
	if configMount.MountPath != "/etc/containerd" {
		t.Errorf("volumeMount MountPath should be /etc/containerd, got %s", configMount.MountPath)
	}
}

func TestBuildRegistryConfigReconcilerDaemonset_K3sRuntime(t *testing.T) {
	builder := newTestBuilder(t)
	reg := newTestRegistry("test-reg", 5000)
	ds := builder.BuildRegistryConfigReconcilerDaemonset(context.Background(), reg, "test-cm", "registries.json", registry.RuntimeK3s)

	podSpec := ds.Spec.Template.Spec

	// Verify hostPath volume points to k3s config directory
	var hostPathVolume *corev1.HostPathVolumeSource
	for _, v := range podSpec.Volumes {
		if v.HostPath != nil {
			hostPathVolume = v.HostPath
			break
		}
	}
	if hostPathVolume == nil {
		t.Fatal("expected a hostPath volume")
	}
	if hostPathVolume.Path != "/etc/rancher/k3s" {
		t.Errorf("hostPath should be /etc/rancher/k3s for k3s runtime, got %s", hostPathVolume.Path)
	}
	if hostPathVolume.Type == nil || *hostPathVolume.Type != corev1.HostPathDirectoryOrCreate {
		t.Errorf("hostPath type should be DirectoryOrCreate for k3s runtime, got %v", hostPathVolume.Type)
	}

	// Verify container args include k3s runtime flag
	container := podSpec.Containers[0]
	argsStr := strings.Join(container.Args, " ")
	if !strings.Contains(argsStr, "--runtime=k3s") {
		t.Errorf("container args should include --runtime=k3s, got %v", container.Args)
	}
	if !strings.Contains(argsStr, "--config-dir=/etc/rancher/k3s") {
		t.Errorf("container args should include --config-dir=/etc/rancher/k3s, got %v", container.Args)
	}
	if strings.Contains(argsStr, "--config-file=config.toml") {
		t.Error("container args should NOT include --config-file=config.toml for k3s runtime")
	}

	// Verify volumeMount points to k3s directory
	var configMount *corev1.VolumeMount
	for i, vm := range container.VolumeMounts {
		if vm.Name == "containerd-config" {
			configMount = &container.VolumeMounts[i]
			break
		}
	}
	if configMount == nil {
		t.Fatal("expected containerd-config volume mount")
	}
	if configMount.MountPath != "/etc/rancher/k3s" {
		t.Errorf("volumeMount MountPath should be /etc/rancher/k3s, got %s", configMount.MountPath)
	}

	// Verify k3s-agent-certs volume and mount
	var k3sAgentCertsVolume *corev1.HostPathVolumeSource
	for _, v := range podSpec.Volumes {
		if v.Name == "k3s-agent-certs" {
			k3sAgentCertsVolume = v.HostPath
			break
		}
	}
	if k3sAgentCertsVolume == nil {
		t.Fatal("expected k3s-agent-certs hostPath volume")
	}
	if k3sAgentCertsVolume.Path != "/var/lib/rancher/k3s/agent/etc" {
		t.Errorf("k3s-agent-certs hostPath should be /var/lib/rancher/k3s/agent/etc, got %s", k3sAgentCertsVolume.Path)
	}

	var k3sAgentCertsMount *corev1.VolumeMount
	for i, vm := range container.VolumeMounts {
		if vm.Name == "k3s-agent-certs" {
			k3sAgentCertsMount = &container.VolumeMounts[i]
			break
		}
	}
	if k3sAgentCertsMount == nil {
		t.Fatal("expected k3s-agent-certs volume mount")
	}
	if k3sAgentCertsMount.MountPath != "/var/lib/rancher/k3s/agent/etc" {
		t.Errorf("k3s-agent-certs mount path should be /var/lib/rancher/k3s/agent/etc, got %s", k3sAgentCertsMount.MountPath)
	}

	if !strings.Contains(argsStr, "--k3s-hosts-dir=/var/lib/rancher/k3s/agent/etc") {
		t.Errorf("container args should include --k3s-hosts-dir=/var/lib/rancher/k3s/agent/etc, got %v", container.Args)
	}

	// Security settings should be identical to containerd variant
	if !podSpec.HostPID {
		t.Error("HostPID should be true for k3s runtime")
	}
	if container.SecurityContext.Capabilities == nil {
		t.Fatal("capabilities should not be nil")
	}
	hasKill := false
	for _, cap := range container.SecurityContext.Capabilities.Add {
		if cap == "KILL" {
			hasKill = true
		}
	}
	if !hasKill {
		t.Error("KILL capability should be present for k3s runtime")
	}
}
