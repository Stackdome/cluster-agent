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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.
type WorkspaceResourcePhase string

const (
	WorkspaceResourcePhasePending WorkspaceResourcePhase = "Pending"
	WorkspaceResourcePhaseReady   WorkspaceResourcePhase = "Ready"
	WorkspaceResourcePhaseFailed  WorkspaceResourcePhase = "Failed"
)

// WorkspaceResourceSpec defines the desired state of WorkspaceResource
type WorkspaceResourceSpec struct {
	// Only one of the following fields should be specified
	// +union
	ApplicationSource   *ApplicationSourceSpec   `json:"applicationSource,omitempty"`
	PrebuiltApplication *PrebuiltApplicationSpec `json:"prebuiltApplication,omitempty"`
	Command             string                   `json:"command"`
	// +optional
	Mounts []ResourceMounts `json:"mounts,omitempty"`
	// +optional
	EnvironmentVariables []EnvironmentVariables `json:"environmentVariables,omitempty"`
	// Other resources this workspace resource depends on.
	// +optional
	DependsOn []string `json:"dependOn"`
	// +optional
	Ports         []Port `json:"ports"`
	RunSourceHash string `json:"runSourceHash"`
}

type Port struct {
	Number int32 `json:"number"`
	Expose bool  `json:"expose"`
}

type EnvironmentVariables struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ResourceMounts struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type ApplicationSourceSpec struct {
	Context    string `json:"context"`
	DockerFile string `json:"dockerFile"`
}

type PrebuiltApplicationSpec struct {
	Image string `json:"image"`
}

// WorkspaceResourceStatus defines the observed state of WorkspaceResource
type WorkspaceResourceStatus struct {
	// The most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions is a list of status conditions ths object is in.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// DEPRECATED: This field is not part of any API contract
	// it will go away as soon as kubectl can print conditions!
	// Human readable status - please use .Conditions from code
	Phase           WorkspaceResourcePhase `json:"phase,omitempty"`
	ImageSourceHash string                 `json:"imageSourceHash,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// WorkspaceResource is the Schema for the workspaceresources API
type WorkspaceResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceResourceSpec   `json:"spec,omitempty"`
	Status WorkspaceResourceStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WorkspaceResourceList contains a list of WorkspaceResource
type WorkspaceResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkspaceResource `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkspaceResource{}, &WorkspaceResourceList{})
}
