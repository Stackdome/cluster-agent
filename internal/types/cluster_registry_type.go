package types

// RegistryConfig holds registry mappings to apply to containerd config
type RegistryConfig struct {
	Registries []Registry `json:"registries"`
}

// Registry defines a registry host and endpoint mapping
type Registry struct {
	ServiceIp string `json:"serviceIp"`
	Endpoint  string `json:"endpoint"`
}

func NewRegistryConfig() *RegistryConfig {
	return &RegistryConfig{
		Registries: make([]Registry, 0),
	}
}

func (r *RegistryConfig) HasEndpoint(ep string) bool {
	for _, registry := range r.Registries {
		if registry.Endpoint == ep {
			return true
		}
	}
	return false
}
func (r *RegistryConfig) HasEndpointWithServiceIP(ep string, serviceIP string) bool {
	for _, registry := range r.Registries {
		if registry.Endpoint == ep && registry.ServiceIp == serviceIP {
			return true
		}
	}
	return false
}

func (r *RegistryConfig) ValidRegistries() []Registry {
	var res []Registry
	for _, registry := range r.Registries {
		if len(registry.ServiceIp) == 0 || len(registry.Endpoint) == 0 {
			continue
		}
		res = append(res, registry)
	}
	return res
}

func (r *RegistryConfig) AddRegistry(serviceIP string, endpoint string) bool {
	if r.HasEndpointWithServiceIP(endpoint, serviceIP) {
		return false
	}

	r.RemoveRegistryEndpointEntry(endpoint)

	r.Registries = append(r.Registries, Registry{
		ServiceIp: serviceIP,
		Endpoint:  endpoint,
	})
	return true
}

func (r *RegistryConfig) RemoveRegistryEndpointEntry(ep string) bool {
	if !r.HasEndpoint(ep) {
		return false
	}
	var new []Registry
	for _, reg := range r.Registries {
		if reg.Endpoint != ep {
			new = append(new, reg)
		}
	}

	r.Registries = new
	return true
}
