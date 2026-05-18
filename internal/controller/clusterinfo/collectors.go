package clusterinfo

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	networkv1 "k8s.io/api/networking/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

func quantityToMi(q resource.Quantity) string {
	return fmt.Sprintf("%dMi", q.Value()/(1<<20))
}

func quantityToGi(q resource.Quantity) string {
	return fmt.Sprintf("%dGi", q.Value()/(1<<30))
}

const (
	labelTopologyZone              = "topology.kubernetes.io/zone"
	labelTopologyRegion            = "topology.kubernetes.io/region"
	labelLegacyFailureDomainZone   = "failure-domain.beta.kubernetes.io/zone"
	labelLegacyFailureDomainRegion = "failure-domain.beta.kubernetes.io/region"

	annotationDefaultStorageClass = "storageclass.kubernetes.io/is-default-class"
	annotationDefaultIngressClass = "ingressclass.kubernetes.io/is-default-class"
)

// BuildNodeInfoList converts a list of Kubernetes Nodes into NodeInfo structs.
// Returns the node infos and a deduplicated list of availability zones.
func BuildNodeInfoList(nodes []corev1.Node) ([]corev1alpha1.NodeInfo, []string) {
	infos := make([]corev1alpha1.NodeInfo, 0, len(nodes))
	zoneSet := make(map[string]struct{})

	for _, node := range nodes {
		info := corev1alpha1.NodeInfo{
			Name:     node.Name,
			Ready:    isNodeReady(node),
			Topology: extractTopology(node.Labels),
		}

		if cpu := node.Status.Allocatable[corev1.ResourceCPU]; !cpu.IsZero() {
			info.AllocatableCPU = cpu.String()
		}
		if mem := node.Status.Allocatable[corev1.ResourceMemory]; !mem.IsZero() {
			info.AllocatableMemory = quantityToMi(mem)
		}
		if disk := node.Status.Allocatable[corev1.ResourceEphemeralStorage]; !disk.IsZero() {
			info.AllocatableEphemeralDisk = quantityToGi(disk)
		}
		if cap := node.Status.Capacity[corev1.ResourceEphemeralStorage]; !cap.IsZero() {
			info.CapacityEphemeralDisk = quantityToGi(cap)
		}

		if info.Topology.Zone != "" {
			zoneSet[info.Topology.Zone] = struct{}{}
		}
		infos = append(infos, info)
	}

	zones := make([]string, 0, len(zoneSet))
	for z := range zoneSet {
		zones = append(zones, z)
	}
	sort.Strings(zones)
	return infos, zones
}

func isNodeReady(node corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func extractTopology(labels map[string]string) corev1alpha1.NodeTopology {
	zone := labels[labelTopologyZone]
	if zone == "" {
		zone = labels[labelLegacyFailureDomainZone]
	}
	region := labels[labelTopologyRegion]
	if region == "" {
		region = labels[labelLegacyFailureDomainRegion]
	}
	return corev1alpha1.NodeTopology{Zone: zone, Region: region}
}

// BuildStorageClassInfoList converts a list of StorageClasses into StorageClassInfo structs.
func BuildStorageClassInfoList(scs []storagev1.StorageClass) []corev1alpha1.StorageClassInfo {
	infos := make([]corev1alpha1.StorageClassInfo, 0, len(scs))
	for _, sc := range scs {
		infos = append(infos, corev1alpha1.StorageClassInfo{
			Name:        sc.Name,
			Provisioner: sc.Provisioner,
			IsDefault:   sc.Annotations[annotationDefaultStorageClass] == "true",
		})
	}
	return infos
}

// BuildLoadBalancerInfoList converts LoadBalancer-type Services into LoadBalancerInfo structs.
func BuildLoadBalancerInfoList(svcs []corev1.Service) []corev1alpha1.LoadBalancerInfo {
	infos := make([]corev1alpha1.LoadBalancerInfo, 0)
	for _, svc := range svcs {
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer {
			continue
		}
		info := corev1alpha1.LoadBalancerInfo{
			ServiceName:      svc.Name,
			ServiceNamespace: svc.Namespace,
		}
		for _, ingress := range svc.Status.LoadBalancer.Ingress {
			if ingress.IP != "" {
				info.IngressIPs = append(info.IngressIPs, ingress.IP)
				info.HasIP = true
			}
			if ingress.Hostname != "" {
				info.IngressHostnames = append(info.IngressHostnames, ingress.Hostname)
			}
		}
		infos = append(infos, info)
	}
	return infos
}

// BuildIngressClassInfoList converts a list of IngressClasses into IngressClassInfo structs.
func BuildIngressClassInfoList(ics []networkv1.IngressClass) []corev1alpha1.IngressClassInfo {
	infos := make([]corev1alpha1.IngressClassInfo, 0, len(ics))
	for _, ic := range ics {
		infos = append(infos, corev1alpha1.IngressClassInfo{
			Name:       ic.Name,
			Controller: ic.Spec.Controller,
			IsDefault:  ic.Annotations[annotationDefaultIngressClass] == "true",
		})
	}
	return infos
}
