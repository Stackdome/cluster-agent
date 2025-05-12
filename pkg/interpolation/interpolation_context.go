package interpolation

import (
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

type InterpolationContext struct {
	Resources map[string]ResourceContext        `json:"resources"`
	Runtime   map[string]map[string]interface{} `json:"runtime"`
}

type ResourceContext struct {
	Name   string         `json:"name"`
	Status ResourceStatus `json:"status"`
}

type ResourceStatus struct {
	InternalService *string          `json:"internal_service"`
	PublicIngresses []IngressContext `json:"public_ingresses"`
}

type IngressContext struct {
	URL        string `json:"url"`
	TargetPort int    `json:"target_port"`
}

func NewInterpolationContext(resources []*corev1alpha1.StackResource) (*InterpolationContext, error) {
	ctx := &InterpolationContext{
		Resources: make(map[string]ResourceContext),
		Runtime:   make(map[string]map[string]interface{}),
	}

	for _, resource := range resources {
		ctx.Resources[resource.Name] = ResourceContext{
			Name: resource.Name,
			Status: ResourceStatus{
				InternalService: resource.Status.InternalAddress,
				PublicIngresses: buildPublicIngressContext(resource.Spec.Ports),
			},
		}
	}
	return ctx, nil
}

func buildPublicIngressContext(in []corev1alpha1.Port) []IngressContext {
	out := make([]IngressContext, len(in))
	for i, ingress := range in {
		if ingress.ExposeToPublic {
			out[i] = IngressContext{
				URL:        ingress.FQDN,
				TargetPort: int(ingress.Number),
			}
		}
	}
	return out
}
