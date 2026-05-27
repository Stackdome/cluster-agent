package main

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestReconcileK3sRegistries_CreatesFromScratch(t *testing.T) {
	tmpDir := t.TempDir()
	configDir = &tmpDir

	reconciler := NewContainerdConfigReconciler(tmpDir, "config.toml", "/dev/null", 30)

	registries := []Registry{
		{ServiceIp: "10.43.1.5", Endpoint: "http://zot-registry.stackdome-registry.svc.cluster.local"},
	}

	if err := reconciler.reconcileK3sRegistries(registries); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "registries.yaml"))
	if err != nil {
		t.Fatalf("failed to read registries.yaml: %v", err)
	}

	var config k3sRegistriesConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatalf("failed to parse registries.yaml: %v", err)
	}

	mirrorKey := "zot-registry.stackdome-registry.svc.cluster.local"
	mirror, ok := config.Mirrors[mirrorKey]
	if !ok {
		t.Fatalf("expected mirror key %q, got keys: %v", mirrorKey, config.Mirrors)
	}
	if len(mirror.Endpoint) != 1 || mirror.Endpoint[0] != "http://10.43.1.5" {
		t.Errorf("expected endpoint [http://10.43.1.5], got %v", mirror.Endpoint)
	}

	// Verify configs section marks registry as insecure
	regConf, ok := config.Configs[mirrorKey]
	if !ok {
		t.Fatalf("expected configs entry for %q", mirrorKey)
	}
	if !regConf.TLS.InsecureSkipVerify {
		t.Error("insecure_skip_verify should be true")
	}
}

func TestReconcileK3sRegistries_PreservesUserEntries(t *testing.T) {
	tmpDir := t.TempDir()
	configDir = &tmpDir

	// Write existing registries.yaml with a user-configured mirror
	existing := k3sRegistriesConfig{
		Mirrors: map[string]k3sMirror{
			"docker.io": {
				Endpoint: []string{"https://my-mirror.example.com"},
			},
		},
	}
	data, err := yaml.Marshal(existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "registries.yaml"), data, 0644); err != nil {
		t.Fatal(err)
	}

	reconciler := NewContainerdConfigReconciler(tmpDir, "config.toml", "/dev/null", 30)

	registries := []Registry{
		{ServiceIp: "10.43.1.5", Endpoint: "http://zot-registry.stackdome-registry.svc.cluster.local"},
	}

	if err := reconciler.reconcileK3sRegistries(registries); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err = os.ReadFile(filepath.Join(tmpDir, "registries.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var config k3sRegistriesConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}

	// User entry should be preserved
	if _, ok := config.Mirrors["docker.io"]; !ok {
		t.Error("user-configured docker.io mirror should be preserved")
	}

	// Stackdome entry should be added
	if _, ok := config.Mirrors["zot-registry.stackdome-registry.svc.cluster.local"]; !ok {
		t.Error("stackdome mirror entry should be added")
	}
}

