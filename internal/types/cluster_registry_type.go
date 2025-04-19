package types

import "net/url"

// RegistryConfig holds registry mappings to apply to containerd config
type RegistryConfig struct {
	Registries []Registry `json:"registries"`
}

// Registry defines a registry host and endpoint mapping
type Registry struct {
	Host     string `json:"host"`
	Endpoint string `json:"endpoint"`
}

func NewRegistryConfig() *RegistryConfig {
	return &RegistryConfig{
		Registries: make([]Registry, 0),
	}
}

func (r *RegistryConfig) HasHost(host string) bool {
	for _, registry := range r.Registries {
		if registry.Host == host {
			return true
		}
	}
	return false
}

func (r *RegistryConfig) ValidRegistries() []Registry {
	var res []Registry
	for _, registry := range r.Registries {
		if len(registry.Host) == 0 {
			continue
		}
		_, err := url.Parse(registry.Endpoint)
		if err != nil {
			continue
		}
		res = append(res, registry)
	}
	return res
}

func (r *RegistryConfig) AddRegistry(host string, endpoint string) bool {
	if r.HasHost(host) {
		return false
	}

	r.Registries = append(r.Registries, Registry{
		Host:     host,
		Endpoint: endpoint,
	})
	return true
}

func (r *RegistryConfig) RemoveRegistryHostEntry(host string) bool {
	if !r.HasHost(host) {
		return false
	}
	var new []Registry
	for _, reg := range r.Registries {
		if reg.Host != host {
			new = append(new, reg)
		}
	}

	r.Registries = new
	return true
}
