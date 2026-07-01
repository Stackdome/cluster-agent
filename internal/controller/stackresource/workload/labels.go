package workload

import (
	"stackdome.io/cluster-agent/api/core/v1alpha1"
)

func GetWorkloadNameForResource(resource *v1alpha1.StackResource) string {
	return resource.Name
}

func GetWorkloadLabelForResource(resource *v1alpha1.StackResource) map[string]string {
	return map[string]string{
		"resource": GetWorkloadNameForResource(resource),
	}
}

// IdentityLabels copies the Stackdome identity labels from the StackResource
// onto child objects (Deployment, Service) so the Stack controller can discover
// them and so they appear in label-based queries.
func IdentityLabels(resource *v1alpha1.StackResource) map[string]string {
	out := map[string]string{}
	for _, key := range []string{
		v1alpha1.LabelManagedBy,
		v1alpha1.LabelStackName,
		v1alpha1.LabelStackID,
		v1alpha1.LabelResourceName,
		v1alpha1.LabelResourceID,
	} {
		if val, ok := resource.Labels[key]; ok {
			out[key] = val
		}
	}
	return out
}

func mergeLabels(base map[string]string, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
