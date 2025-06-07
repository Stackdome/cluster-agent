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
	StackAvailable StackCondition = "Available"
)

const (
	StackdomeServerObjectRevisionAnnotationKey = "stackdome.stackdome.io/stackdome-server-object-revision"
)

// StackSpec defines the desired state of a Stack
type StackSpec struct {
	// +required
	StackResources []StackResourceTemplate `json:"stackResources"`
}

type StackResourceTemplate struct {
	// +required
	Name string `json:"name"`
	// +required
	Spec StackResourceSpec `json:"spec"`
}

// StackStatus defines the observed state of Stack
type StackStatus struct {
	// The most recent generation observed by the controller.
	ObservedStackdomeServerObjectRevision string `json:"observedStackdomeServerObjectRevision,omitempty"`
	ObservedGeneration                    int64  `json:"observedGeneration,omitempty"`
	// Conditions is a list of status conditions ths object is in.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +kubebuilder:default=Pending
	Phase      StackPhase `json:"phase,omitempty"`
	StatusHash string     `json:"statusHash,omitempty"`
}

func (w *Stack) StatusHash() string {
	hasher := fnv.New32a()
	hasher.Reset()
	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	w.Status.StatusHash = ""
	printer.Fprintf(hasher, "%#v", w.Status)
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum32()))
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// Workspace is the Schema for the workspaces API
type Stack struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StackSpec   `json:"spec,omitempty"`
	Status StackStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// StackList contains a list of Stack
type StackList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Stack `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Stack{}, &StackList{})
}
