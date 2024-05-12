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

type WorkspaceVolumePhase string

const (
	WorkspaceVolumePhasePending WorkspaceVolumePhase = "Pending"
	WorkspaceVolumePhaseReady   WorkspaceVolumePhase = "Ready"
)

type WorkspaceVolumeCondition string

const (
	WorkspaceVolumeConditionAvailable  WorkspaceVolumeCondition = "Available"
	WorkspaceVolumeConditionSyncedOnce WorkspaceVolumeCondition = "SyncedOnce"
)

const (
	LastSyncedAtAnnotation = "workspacevolume.soradev.io/LastSyncedAt"
)

// WorkspaceVolumeSpec defines the desired state of WorkspaceVolume
type WorkspaceVolumeSpec struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Size string              `json:"size"`
	Type ResourceStorageType `json:"type"`
	// +optional
	DontAllowSync bool `json:"dontAllowSync"`
	NeedsSync     bool `json:"needsSync"`
}

// WorkspaceVolumeStatus defines the observed state of WorkspaceVolume
type WorkspaceVolumeStatus struct {
	// The most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions is a list of status conditions ths object is in.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	PvcName    string             `json:"pvcName"`
	// +kubebuilder:default=Pending
	Phase WorkspaceVolumePhase `json:"phase,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// WorkspaceVolume is the Schema for the workspacevolumes API
type WorkspaceVolume struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceVolumeSpec   `json:"spec,omitempty"`
	Status WorkspaceVolumeStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// WorkspaceVolumeList contains a list of WorkspaceVolume
type WorkspaceVolumeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkspaceVolume `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkspaceVolume{}, &WorkspaceVolumeList{})
}
