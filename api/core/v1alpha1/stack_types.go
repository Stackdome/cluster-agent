package v1alpha1

import (
	"fmt"
	"hash/fnv"

	"github.com/davecgh/go-spew/spew"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

type StackPhase string

const (
	StackReady   StackPhase = "Ready"
	StackPending StackPhase = "Pending"
	StackFailed  StackPhase = "Failed"
)

type StackCondition string

const (
	StackConditionAvailable      StackCondition = "Available"
	StackConditionResourcesReady StackCondition = "ResourcesReady"
	StackConditionStalled        StackCondition = "Stalled"
)

type StackSpec struct {
	// +required
	// +listType=set
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	ResourceNames []string `json:"resourceNames"`
}

type StackResourceSummary struct {
	Name              string                          `json:"name"`
	Phase             StackResourcePhase              `json:"phase,omitempty"`
	ObservedRevision  string                          `json:"observedRevision,omitempty"`
	ConvergedRevision string                          `json:"convergedRevision,omitempty"`
	LastConverged     *StackResourceConvergenceRecord `json:"lastConverged,omitempty"`
	AvailableReplicas int32                           `json:"availableReplicas,omitempty"`
	UpdatedReplicas   int32                           `json:"updatedReplicas,omitempty"`
	Replicas          int32                           `json:"replicas,omitempty"`
	Missing           bool                            `json:"missing,omitempty"`
	Message           string                          `json:"message,omitempty"`
}

type StackStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	TargetRevision     string             `json:"targetRevision,omitempty"`
	LastConverged      *ConvergenceRecord `json:"lastConverged,omitempty"`
	// +kubebuilder:default=Pending
	Phase             StackPhase             `json:"phase,omitempty"`
	Conditions        []metav1.Condition     `json:"conditions,omitempty"`
	Resources         []StackResourceSummary `json:"resources,omitempty"`
	OrphanedResources []string               `json:"orphanedResources,omitempty"`
	StatusHash        string                 `json:"statusHash,omitempty"`
}

type ConvergenceRecord struct {
	Revision  string      `json:"revision"`
	ReleaseID string      `json:"releaseId,omitempty"`
	At        metav1.Time `json:"at"`
}

type StackResourceConvergenceRecord struct {
	Revision string      `json:"revision"`
	At       metav1.Time `json:"at"`
}

func (w *Stack) StatusHash() string {
	hasher := fnv.New64a()
	hasher.Reset()
	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	statusCopy := w.Status
	statusCopy.StatusHash = ""
	conds := make([]metav1.Condition, len(w.Status.Conditions))
	copy(conds, w.Status.Conditions)
	for i := range conds {
		conds[i].LastTransitionTime = metav1.Time{}
	}
	statusCopy.Conditions = conds
	printer.Fprintf(hasher, "%#v", statusCopy)
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum64()))
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
type Stack struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StackSpec   `json:"spec,omitempty"`
	Status StackStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

type StackList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Stack `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Stack{}, &StackList{})
}
