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
	"time"

	"github.com/davecgh/go-spew/spew"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

type ResourceRef string

func (r ResourceRef) String() string {
	return string(r)
}

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
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Size string `json:"size"`
	// +optional
	StorageClass string `json:"storageClass"`
	// +optional
	Source             *VolumeSource `json:"source,omitempty"`
	NeedsSyncBeforeUse bool          `json:"needsSyncBeforeUse"`
}

type VolumeSource struct {
	// +optional
	LocalDir *LocalDirSource `json:"localDir,omitempty"`

	// +optional
	BuildArtifacts []BuildArtifactSource `json:"buildArtifacts,omitempty"`
}

type LocalDirSource struct {
	Path          string `json:"path"`
	ContinousSync bool   `json:"continousSync"`
}

type BuildArtifactSource struct {
	ResourceRef     ResourceRef `json:"resourceRef"`
	SourcePath      string      `json:"sourcePath"`
	DestinationPath string      `json:"destinationPath"`
}

// WorkspaceVolumeStatus defines the observed state of WorkspaceVolume
type WorkspaceVolumeStatus struct {
	// The most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions is a list of status conditions ths object is in.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	PvcName    string             `json:"pvcName"`
	// +kubebuilder:default=Pending
	Phase        WorkspaceVolumePhase `json:"phase,omitempty"`
	LastSyncedAt *metav1.Time         `json:"LastSyncedAt,omitempty"`
	// +optional
	BuildArtifactSyncs map[ResourceRef]BuildArtifactSyncInfo `json:"buildArtifactSyncs,omitempty"`
	StatusHash         string                                `json:"statusHash,omitempty"`
}

func (w *WorkspaceVolume) StatusHash() string {
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

type BuildArtifactSyncInfo struct {
	BuildID string                  `json:"buildID"`
	Status  BuildArtifactSyncStatus `json:"status"`
}

type BuildArtifactSyncStatus string

const (
	BuildArtifactSyncStatusPending    BuildArtifactSyncStatus = "Pending"
	BuildArtifactSyncStatusInProgress BuildArtifactSyncStatus = "InProgress"
	BuildArtifactSyncStatusCompleted  BuildArtifactSyncStatus = "Completed"
	BuildArtifactSyncStatusFailed     BuildArtifactSyncStatus = "Failed"
)

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

func (w *WorkspaceVolume) MarkAsSynced() {
	if w.Annotations == nil {
		w.Annotations = map[string]string{}
	}
	w.Annotations[LastSyncedAtAnnotation] = metav1.NewTime(time.Now().UTC()).String()
}

func (s *WorkspaceVolumeStatus) SetBuildArtifactSyncStatus(resourceRef ResourceRef, buildID string, status BuildArtifactSyncStatus) {
	if s.BuildArtifactSyncs == nil {
		s.BuildArtifactSyncs = map[ResourceRef]BuildArtifactSyncInfo{}
	}
	s.BuildArtifactSyncs[resourceRef] = BuildArtifactSyncInfo{
		BuildID: buildID,
		Status:  status,
	}
}

func init() {
	SchemeBuilder.Register(&WorkspaceVolume{}, &WorkspaceVolumeList{})
}
