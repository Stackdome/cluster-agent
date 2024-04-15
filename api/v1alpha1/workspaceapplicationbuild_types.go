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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type WorkspaceApplicationBuildPhase string

const (
	WorkspaceApplicationBuildPhaseSuccess WorkspaceApplicationBuildPhase = "Success"
	WorkspaceApplicationBuildPhaseFailed  WorkspaceApplicationBuildPhase = "Failed"
	WorkspaceApplicationBuildPhasePending WorkspaceApplicationBuildPhase = "Pending"
)

type WorkspaceApplicationBuildStatusCondition string

const (
	WorkspaceApplicationBuildAvailable WorkspaceApplicationBuildStatusCondition = "Available"
)

// WorkspaceApplicationBuildSpec defines the desired state of WorkspaceApplicationBuild
type WorkspaceApplicationBuildSpec struct {
	SourceHash string     `json:"sourceHash"`
	ContextRef ContextRef `json:"contextRef"`
	Registry   string     `json:"registry"`
}

type ContextRef struct {
	WorkspaceStorageName string `json:"workspaceStorageName"`
	DockerfilePath       string `json:"dockerfilePath"`
	ResourceName         string `json:"resourceName"`
	Context              string `json:"context"`
}

// WorkspaceApplicationBuildStatus defines the observed state of WorkspaceApplicationBuild
type WorkspaceApplicationBuildStatus struct {
	// The most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions is a list of status conditions ths object is in.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// DEPRECATED: This field is not part of any API contract
	// it will go away as soon as kubectl can print conditions!
	// Human readable status - please use .Conditions from code
	// +kubebuilder:default=Pending
	Phase           WorkspaceApplicationBuildPhase `json:"phase,omitempty"`
	BuildSourceHash string                         `json:"buildSourceHash,omitempty"`
	ImageUrl        string                         `json:"imageUrl"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=wab
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// WorkspaceApplicationBuild is the Schema for the workspaceapplicationbuilds API
type WorkspaceApplicationBuild struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceApplicationBuildSpec   `json:"spec,omitempty"`
	Status WorkspaceApplicationBuildStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WorkspaceApplicationBuildList contains a list of WorkspaceApplicationBuild
type WorkspaceApplicationBuildList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkspaceApplicationBuild `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkspaceApplicationBuild{}, &WorkspaceApplicationBuildList{})
}
