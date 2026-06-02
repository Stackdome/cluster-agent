package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func TestReconcileK3sRegistries_CreatesHostsToml(t *testing.T) {
	tmpDir := t.TempDir()
	k3sHostsDir = &tmpDir

	reconciler := NewContainerdConfigReconciler(tmpDir, "config.toml", "/dev/null", 30)

	registries := []Registry{
		{ServiceIp: "10.43.1.5", Endpoint: "http://zot-registry.stackdome-registry.svc.cluster.local"},
	}

	if err := reconciler.reconcileK3sRegistries(registries); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hostsTomlPath := filepath.Join(tmpDir, "containerd", "certs.d", "zot-registry.stackdome-registry.svc.cluster.local", "hosts.toml")
	data, err := os.ReadFile(hostsTomlPath)
	if err != nil {
		t.Fatalf("expected hosts.toml at %s: %v", hostsTomlPath, err)
	}

	var config map[string]interface{}
	if _, err := toml.Decode(string(data), &config); err != nil {
		t.Fatalf("failed to parse hosts.toml: %v", err)
	}

	server, ok := config["server"].(string)
	if !ok || server != "http://zot-registry.stackdome-registry.svc.cluster.local" {
		t.Errorf("expected server = registry endpoint, got %v", config["server"])
	}

	hosts, ok := config["host"].(map[string]interface{})
	if !ok {
		t.Fatal("expected [host] section in hosts.toml")
	}

	hostEntry, ok := hosts["http://10.43.1.5"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected host entry for http://10.43.1.5, got keys: %v", hosts)
	}

	caps, ok := hostEntry["capabilities"].([]interface{})
	if !ok || len(caps) != 2 {
		t.Errorf("expected capabilities [pull, resolve], got %v", hostEntry["capabilities"])
	}

	skipVerify, ok := hostEntry["skip_verify"].(bool)
	if !ok || !skipVerify {
		t.Errorf("expected skip_verify = true, got %v", hostEntry["skip_verify"])
	}
}

func TestReconcileK3sRegistries_NoopWhenUnchanged(t *testing.T) {
	tmpDir := t.TempDir()
	k3sHostsDir = &tmpDir

	reconciler := NewContainerdConfigReconciler(tmpDir, "config.toml", "/dev/null", 30)

	registries := []Registry{
		{ServiceIp: "10.43.1.5", Endpoint: "http://zot-registry.stackdome-registry.svc.cluster.local"},
	}

	if err := reconciler.reconcileK3sRegistries(registries); err != nil {
		t.Fatalf("unexpected error on first call: %v", err)
	}

	hostsTomlPath := filepath.Join(tmpDir, "containerd", "certs.d", "zot-registry.stackdome-registry.svc.cluster.local", "hosts.toml")
	info1, err := os.Stat(hostsTomlPath)
	if err != nil {
		t.Fatalf("expected hosts.toml after first call: %v", err)
	}

	time.Sleep(1 * time.Second)

	if err := reconciler.reconcileK3sRegistries(registries); err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}

	info2, err := os.Stat(hostsTomlPath)
	if err != nil {
		t.Fatalf("expected hosts.toml after second call: %v", err)
	}

	if info2.ModTime() != info1.ModTime() {
		t.Error("hosts.toml was rewritten despite no changes — expected noop")
	}
}

func TestReconcileK3sRegistries_RewritesOnChange(t *testing.T) {
	tmpDir := t.TempDir()
	k3sHostsDir = &tmpDir

	reconciler := NewContainerdConfigReconciler(tmpDir, "config.toml", "/dev/null", 30)

	registries := []Registry{
		{ServiceIp: "10.43.1.5", Endpoint: "http://zot-registry.stackdome-registry.svc.cluster.local"},
	}

	if err := reconciler.reconcileK3sRegistries(registries); err != nil {
		t.Fatalf("unexpected error on first call: %v", err)
	}

	hostsTomlPath := filepath.Join(tmpDir, "containerd", "certs.d", "zot-registry.stackdome-registry.svc.cluster.local", "hosts.toml")
	original, err := os.ReadFile(hostsTomlPath)
	if err != nil {
		t.Fatalf("expected hosts.toml after first call: %v", err)
	}

	registries[0].ServiceIp = "10.43.1.99"
	if err := reconciler.reconcileK3sRegistries(registries); err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}

	updated, err := os.ReadFile(hostsTomlPath)
	if err != nil {
		t.Fatalf("expected hosts.toml after second call: %v", err)
	}

	if string(updated) == string(original) {
		t.Error("hosts.toml should have been rewritten after ServiceIp change")
	}

	var config map[string]interface{}
	if _, err := toml.Decode(string(updated), &config); err != nil {
		t.Fatalf("failed to parse updated hosts.toml: %v", err)
	}
	hosts := config["host"].(map[string]interface{})
	if _, ok := hosts["http://10.43.1.99"]; !ok {
		t.Errorf("expected host entry for new ServiceIp, got keys: %v", hosts)
	}
}

func TestReconcileK3sRegistries_MultipleRegistries(t *testing.T) {
	tmpDir := t.TempDir()
	k3sHostsDir = &tmpDir

	reconciler := NewContainerdConfigReconciler(tmpDir, "config.toml", "/dev/null", 30)

	registries := []Registry{
		{ServiceIp: "10.43.1.5", Endpoint: "http://zot-registry.stackdome-registry.svc.cluster.local"},
		{ServiceIp: "10.43.2.10", Endpoint: "http://second-registry.stackdome-registry.svc.cluster.local"},
	}

	if err := reconciler.reconcileK3sRegistries(registries); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, reg := range registries {
		hostsTomlPath := filepath.Join(tmpDir, "containerd", "certs.d", "zot-registry.stackdome-registry.svc.cluster.local", "hosts.toml")
		if reg.ServiceIp == "10.43.2.10" {
			hostsTomlPath = filepath.Join(tmpDir, "containerd", "certs.d", "second-registry.stackdome-registry.svc.cluster.local", "hosts.toml")
		}
		if _, err := os.Stat(hostsTomlPath); err != nil {
			t.Errorf("expected hosts.toml for %s at %s: %v", reg.Endpoint, hostsTomlPath, err)
		}
	}
}
