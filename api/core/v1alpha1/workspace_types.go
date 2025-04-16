/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"fmt"
	"hash/fnv"

	"github.com/davecgh/go-spew/spew"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

type WorkspacePhase string

const (
	WorkspaceReady   WorkspacePhase = "Ready"
	WorkspacePending WorkspacePhase = "Pending"
	WorkspaceFailed  WorkspacePhase = "Failed"
)

type WorkspaceCondition string

const (
	WorkspaceAvailable WorkspaceCondition = "Available"
)

// WorkspaceSpec defines the desired state of Workspace
type WorkspaceSpec struct {
	Resources    []ResourceSpec `json:"resources"`
	UserName     string         `json:"userName"`
	Organisation string         `json:"organisation"`
	// SLD+TLD. Example: example.io
	// Should not be empty and is a valid domain.
	Domain string `json:"domain"`
}

type ResourceSpec struct {
	Name string                `json:"name"`
	Spec WorkspaceResourceSpec `json:"spec"`
}

// WorkspaceStatus defines the observed state of Workspace
type WorkspaceStatus struct {
	// The most recent generation observed by the controller.
	ObservedStackdomeServerObjectGeneration int64 `json:"observedStackdomeServerObjectGeneration,omitempty"`
	ObservedGeneration                      int64 `json:"observedGeneration,omitempty"`
	// Conditions is a list of status conditions ths object is in.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +kubebuilder:default=Pending
	Phase      WorkspacePhase `json:"phase,omitempty"`
	StatusHash string         `json:"statusHash,omitempty"`
}

func (w *Workspace) StatusHash() string {
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
type Workspace struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceSpec   `json:"spec,omitempty"`
	Status WorkspaceStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WorkspaceList contains a list of Workspace
type WorkspaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workspace `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workspace{}, &WorkspaceList{})
}

func WorkspaceResourceName(resourceName string) string {
	return resourceName
}
