package v1alpha1

import (
	"fmt"
	"hash/fnv"

	"github.com/davecgh/go-spew/spew"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

const (
	ClusterInfoSingletonName      = "stackdome-cluster-info"
	ClusterInfoDefaultLBNamespace = "stackdome-control-plane"
)

type ClusterInfoPhase string

const (
	ClusterInfoPhaseReady      ClusterInfoPhase = "Ready"
	ClusterInfoPhaseRefreshing ClusterInfoPhase = "Refreshing"
	ClusterInfoPhaseUnknown    ClusterInfoPhase = "Unknown"
)

type ClusterInfoSpec struct {
	// Namespaces to search for LoadBalancer-type Services (e.g. where Traefik runs).
	// +kubebuilder:default={"stackdome-control-plane"}
	LoadBalancerNamespaces []string `json:"loadBalancerNamespaces,omitempty"`
}

type ClusterInfoStatus struct {
	Phase             ClusterInfoPhase   `json:"phase,omitempty"`
	StatusHash        string             `json:"statusHash,omitempty"`
	LastRefreshedAt   *metav1.Time       `json:"lastRefreshedAt,omitempty"`
	KubernetesVersion string             `json:"kubernetesVersion,omitempty"`
	TotalNodes        int                `json:"totalNodes,omitempty"`
	ReadyNodes        int                `json:"readyNodes,omitempty"`
	AvailabilityZones []string           `json:"availabilityZones,omitempty"`
	Nodes             []NodeInfo         `json:"nodes,omitempty"`
	StorageClasses    []StorageClassInfo `json:"storageClasses,omitempty"`
	LoadBalancers     []LoadBalancerInfo `json:"loadBalancers,omitempty"`
	IngressClasses    []IngressClassInfo `json:"ingressClasses,omitempty"`
}

type NodeInfo struct {
	Name                     string       `json:"name"`
	Ready                    bool         `json:"ready"`
	AllocatableCPU           string       `json:"allocatableCpu"`
	AllocatableMemory        string       `json:"allocatableMemory"`
	AllocatableEphemeralDisk string       `json:"allocatableEphemeralDisk"`
	CapacityEphemeralDisk    string       `json:"capacityEphemeralDisk"`
	Topology                 NodeTopology `json:"topology,omitempty"`
}

type NodeTopology struct {
	Zone   string `json:"zone,omitempty"`
	Region string `json:"region,omitempty"`
}

type StorageClassInfo struct {
	Name        string `json:"name"`
	Provisioner string `json:"provisioner"`
	IsDefault   bool   `json:"isDefault"`
}

type LoadBalancerInfo struct {
	ServiceName      string   `json:"serviceName"`
	ServiceNamespace string   `json:"serviceNamespace"`
	IngressIPs       []string `json:"ingressIPs,omitempty"`
	IngressHostnames []string `json:"ingressHostnames,omitempty"`
	HasIP            bool     `json:"hasIP"`
}

type IngressClassInfo struct {
	Name       string `json:"name"`
	Controller string `json:"controller"`
	IsDefault  bool   `json:"isDefault"`
}

func (c *ClusterInfo) ComputeStatusHash() string {
	hasher := fnv.New32a()
	hasher.Reset()
	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	saved := c.Status.StatusHash
	c.Status.StatusHash = ""
	printer.Fprintf(hasher, "%#v", c.Status)
	c.Status.StatusHash = saved
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum32()))
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="K8s Version",type="string",JSONPath=".status.kubernetesVersion"
// +kubebuilder:printcolumn:name="Nodes",type="integer",JSONPath=".status.totalNodes"
// +kubebuilder:printcolumn:name="Ready Nodes",type="integer",JSONPath=".status.readyNodes"
// +kubebuilder:printcolumn:name="Last Refreshed",type="date",JSONPath=".status.lastRefreshedAt"
type ClusterInfo struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClusterInfoSpec   `json:"spec,omitempty"`
	Status            ClusterInfoStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterInfoList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterInfo `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterInfo{}, &ClusterInfoList{})
}
