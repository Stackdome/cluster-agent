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

type WorkspaceStatePhase string

const (
	Ready   WorkspaceStatePhase = "Ready"
	Pending WorkspaceStatePhase = "Pending"
	Failed  WorkspaceStatePhase = "Failed"
)

type WorkspaceStateCondition string

const (
	WorkspaceStateConditionAvailable WorkspaceStateCondition = "Available"
)

type WorkspaceResourceStorageStatus string

const (
	Provisioned      WorkspaceResourceStorageStatus = "Provisioned"
	ProvisionPending WorkspaceResourceStorageStatus = "ProvisionPending"
	ProvisionFailed  WorkspaceResourceStorageStatus = "ProvisionFailed"
)

// WorkspaceStateSpec defines the desired state of WorkspaceState
type WorkspaceStateSpec struct {
	// +kubebuilder:validation:Required
	Resources []WorkspaceResourceStorage `json:"resources,omitempty"`
}

type WorkspaceResourceStorage struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Size string `json:"size"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`

	Hash string `json:"hash,omitempty"`
}

// WorkspaceStateStatus defines the observed state of WorkspaceState
type WorkspaceStateStatus struct {
	// The most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions is a list of status conditions ths object is in.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// DEPRECATED: This field is not part of any API contract
	// it will go away as soon as kubectl can print conditions!
	// Human readable status - please use .Conditions from code
	Phase WorkspaceStatePhase `json:"phase,omitempty"`
	// Tracks last reported upgrade policy status.
	// +optional
	WorkspaceStateInfo []ResourceStateInfo `json:"workspaceStateInfo,omitempty"`
}

type ResourceStateInfo struct {
	Name              string                         `json:"name"`
	Status            WorkspaceResourceStorageStatus `json:"resource_status"`
	AddressIdentifier string                         `json:"addressIdentifier"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// WorkspaceState is the Schema for the workspacestates API
type WorkspaceState struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceStateSpec   `json:"spec,omitempty"`
	Status WorkspaceStateStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WorkspaceStateList contains a list of WorkspaceState
type WorkspaceStateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkspaceState `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkspaceState{}, &WorkspaceStateList{})
}
