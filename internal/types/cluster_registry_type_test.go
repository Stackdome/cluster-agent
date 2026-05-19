package types

import "testing"

func TestNewRegistryConfig(t *testing.T) {
	rc := NewRegistryConfig()
	if rc == nil {
		t.Fatal("expected non-nil RegistryConfig")
	}
	if len(rc.Registries) != 0 {
		t.Fatalf("expected empty registries, got %d", len(rc.Registries))
	}
}

func TestAddRegistry(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*RegistryConfig)
		serviceIP  string
		endpoint   string
		wantResult bool
		wantLen    int
	}{
		{
			name:       "new entry returns true",
			setup:      func(rc *RegistryConfig) {},
			serviceIP:  "10.0.0.1",
			endpoint:   "registry.example.com",
			wantResult: true,
			wantLen:    1,
		},
		{
			name: "duplicate returns false",
			setup: func(rc *RegistryConfig) {
				rc.AddRegistry("10.0.0.1", "registry.example.com")
			},
			serviceIP:  "10.0.0.1",
			endpoint:   "registry.example.com",
			wantResult: false,
			wantLen:    1,
		},
		{
			name: "same endpoint different ServiceIP replaces and returns true",
			setup: func(rc *RegistryConfig) {
				rc.AddRegistry("10.0.0.1", "registry.example.com")
			},
			serviceIP:  "10.0.0.2",
			endpoint:   "registry.example.com",
			wantResult: true,
			wantLen:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := NewRegistryConfig()
			tt.setup(rc)
			got := rc.AddRegistry(tt.serviceIP, tt.endpoint)
			if got != tt.wantResult {
				t.Errorf("AddRegistry() = %v, want %v", got, tt.wantResult)
			}
			if len(rc.Registries) != tt.wantLen {
				t.Errorf("len(Registries) = %d, want %d", len(rc.Registries), tt.wantLen)
			}
		})
	}
}

func TestRemoveRegistryEndpointEntry(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*RegistryConfig)
		endpoint   string
		wantResult bool
		wantLen    int
	}{
		{
			name: "removes existing returns true",
			setup: func(rc *RegistryConfig) {
				rc.AddRegistry("10.0.0.1", "registry.example.com")
			},
			endpoint:   "registry.example.com",
			wantResult: true,
			wantLen:    0,
		},
		{
			name:       "non-existent returns false",
			setup:      func(rc *RegistryConfig) {},
			endpoint:   "registry.example.com",
			wantResult: false,
			wantLen:    0,
		},
		{
			name: "only removes matching endpoint",
			setup: func(rc *RegistryConfig) {
				rc.AddRegistry("10.0.0.1", "registry.example.com")
				rc.AddRegistry("10.0.0.2", "other.example.com")
			},
			endpoint:   "registry.example.com",
			wantResult: true,
			wantLen:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := NewRegistryConfig()
			tt.setup(rc)
			got := rc.RemoveRegistryEndpointEntry(tt.endpoint)
			if got != tt.wantResult {
				t.Errorf("RemoveRegistryEndpointEntry() = %v, want %v", got, tt.wantResult)
			}
			if len(rc.Registries) != tt.wantLen {
				t.Errorf("len(Registries) = %d, want %d", len(rc.Registries), tt.wantLen)
			}
		})
	}
}

func TestValidRegistries(t *testing.T) {
	rc := NewRegistryConfig()
	rc.Registries = []Registry{
		{ServiceIp: "10.0.0.1", Endpoint: "registry.example.com"},
		{ServiceIp: "", Endpoint: "missing-ip.example.com"},
		{ServiceIp: "10.0.0.3", Endpoint: ""},
		{ServiceIp: "10.0.0.4", Endpoint: "valid.example.com"},
	}

	valid := rc.ValidRegistries()
	if len(valid) != 2 {
		t.Fatalf("expected 2 valid registries, got %d", len(valid))
	}
	if valid[0].Endpoint != "registry.example.com" {
		t.Errorf("expected first valid endpoint 'registry.example.com', got %q", valid[0].Endpoint)
	}
	if valid[1].Endpoint != "valid.example.com" {
		t.Errorf("expected second valid endpoint 'valid.example.com', got %q", valid[1].Endpoint)
	}
}

func TestHasEndpointWithServiceIP(t *testing.T) {
	rc := NewRegistryConfig()
	rc.AddRegistry("10.0.0.1", "registry.example.com")

	tests := []struct {
		name      string
		endpoint  string
		serviceIP string
		want      bool
	}{
		{"matching pair", "registry.example.com", "10.0.0.1", true},
		{"wrong serviceIP", "registry.example.com", "10.0.0.99", false},
		{"wrong endpoint", "unknown.example.com", "10.0.0.1", false},
		{"both wrong", "unknown.example.com", "10.0.0.99", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rc.HasEndpointWithServiceIP(tt.endpoint, tt.serviceIP)
			if got != tt.want {
				t.Errorf("HasEndpointWithServiceIP(%q, %q) = %v, want %v", tt.endpoint, tt.serviceIP, got, tt.want)
			}
		})
	}
}
