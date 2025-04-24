package v1alpha1

import (
	"fmt"
	"hash/fnv"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/davecgh/go-spew/spew"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

type VolumePhase string

const (
	VolumePhasePending VolumePhase = "Pending"
	VolumePhaseReady   VolumePhase = "Ready"
)

type VolumeCondition string

const (
	VolumeConditionAvailable  VolumeCondition = "Available"
	VolumeConditionSyncedOnce VolumeCondition = "SyncedOnce"
)

const (
	LastSyncedAtAnnotation = "volume.stackdome.io/LastSyncedAt"
)

// WorkspaceVolumeSpec defines the desired state of WorkspaceVolume
type VolumeSpec struct {
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
	Source *VolumeSource `json:"source,omitempty"`
	// +required
	NeedsSyncBeforeUse bool `json:"needsSyncBeforeUse"`
	// +required
	AccessMode corev1.PersistentVolumeAccessMode `json:"accessMode"`
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

// BuildArtifactSource defines how to copy artifacts from a application built to a volume
type BuildArtifactSource struct {
	// BuildSource references the stack resource whose build artifacts should be copied.
	// This must reference a StackResource with ApplicationBuildSpec defined.
	// +required
	BuildSource StackResourceReference `json:"buildSource"`

	// SourcePath is the path within the built image where artifacts are located
	// +required
	SourcePath string `json:"sourcePath"`

	// DestinationPath is the path within the volume where artifacts should be synced
	// +required
	DestinationPath string `json:"destinationPath"`
}

type StackResourceReference struct {
	// Name of the StackResource
	Name string `json:"name"`
}

// VolumeStatus defines the observed state of WorkspaceVolume
type VolumeStatus struct {
	// The most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions is a list of status conditions ths object is in.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	PvcName    string             `json:"pvcName"`
	// +kubebuilder:default=Pending
	Phase        VolumePhase  `json:"phase,omitempty"`
	LastSyncedAt *metav1.Time `json:"LastSyncedAt,omitempty"`
	// +optional
	BuildArtifactSyncs map[string]BuildArtifactSyncInfo `json:"buildArtifactSyncs,omitempty"`
	StatusHash         string                           `json:"statusHash,omitempty"`
}

func (w *Volume) StatusHash() string {
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

// Volume is the Schema for the volumes API
type Volume struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VolumeSpec   `json:"spec,omitempty"`
	Status VolumeStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VolumeList contains a list of Volumes
type VolumeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Volume `json:"items"`
}

func (w *Volume) MarkAsSynced() {
	if w.Annotations == nil {
		w.Annotations = map[string]string{}
	}
	w.Annotations[LastSyncedAtAnnotation] = metav1.NewTime(time.Now().UTC()).String()
}

func (s *VolumeStatus) SetBuildArtifactSyncStatus(stackResourceRef StackResourceReference, buildID string, status BuildArtifactSyncStatus) {
	if s.BuildArtifactSyncs == nil {
		s.BuildArtifactSyncs = map[string]BuildArtifactSyncInfo{}
	}
	s.BuildArtifactSyncs[stackResourceRef.Name] = BuildArtifactSyncInfo{
		BuildID: buildID,
		Status:  status,
	}
}

func init() {
	SchemeBuilder.Register(&Volume{}, &VolumeList{})
}
