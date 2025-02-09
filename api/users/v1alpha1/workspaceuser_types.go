package v1alpha1

import (
	"fmt"
	"hash/fnv"

	"github.com/davecgh/go-spew/spew"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

type WorkspaceUserPhase string

const (
	WorkspaceUserPhasePhasePending WorkspaceUserPhase = "Pending"
	WorkspaceUserPhasePhaseReady   WorkspaceUserPhase = "Ready"
)

const (
	WorkspaceUserAvailable = "Available"
)

// WorkspaceUserSpec defines the desired state of WorkspaceUser
type WorkspaceUserSpec struct {
	// +kubebuilder:validation:MinItems=1
	Namespaces  []string            `json:"namespaces"`
	Username    string              `json:"username"`
	AccessRules []rbacv1.PolicyRule `json:"accessRules"`
	// +optional
	BaseDomain string `json:"baseDomain"`
}

// WorkspaceUserStatus defines the observed state of WorkspaceUser
type WorkspaceUserStatus struct {
	ObservedStackdomeServerObjectGeneration int64 `json:"observedStackdomeServerObjectGeneration,omitempty"`
	// Conditions is a list of status conditions this object is in.
	Conditions          []metav1.Condition `json:"conditions,omitempty"`
	Namespaces          []string           `json:"namespaces,omitempty"`
	ServiceAccountName  string             `json:"serviceAccountName,omitempty"`
	ServiceAccountToken string             `json:"serviceAccountToken,omitempty"`
	// +kubebuilder:default=Pending
	Phase      WorkspaceUserPhase `json:"phase,omitempty"`
	StatusHash string             `json:"statusHash,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster

// WorkspaceUser is the Schema for the workspaceusers API
type WorkspaceUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceUserSpec   `json:"spec,omitempty"`
	Status WorkspaceUserStatus `json:"status,omitempty"`
}

func (user *WorkspaceUser) StatusHash() string {
	hasher := fnv.New32a()
	hasher.Reset()
	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	user.Status.StatusHash = ""
	printer.Fprintf(hasher, "%#v", user.Status)
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum32()))
}

//+kubebuilder:object:root=true

// WorkspaceUserList contains a list of WorkspaceUser
type WorkspaceUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkspaceUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkspaceUser{}, &WorkspaceUserList{})
}
