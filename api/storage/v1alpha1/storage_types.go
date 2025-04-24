package v1alpha1

import (
	"fmt"
	"hash/fnv"

	"github.com/davecgh/go-spew/spew"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

type StoragePhase string

const (
	StorageReady   StoragePhase = "Ready"
	StoragePending StoragePhase = "Pending"
)

type StorageCondition string

type VolumeName string

const (
	StorageAvailable StorageCondition = "Available"
	StorageFailed    StorageCondition = "Failed"
)

const (
	StorageLabel = "storage.stackdome.io/storagename"
)

type StorageSpec struct {
	// +optional
	ProvisionedFor string `json:"provisionedFor"`
	// +kubebuilder:validation:Format:=base64
	// +required
	UserPublicSSHKey string `json:"userPublicSSHKey"`
	// +kubebuilder:validation:Required
	VolumeSpecs map[VolumeName]*VolumeSpec `json:"volumeSpecs,omitempty"`
}

type StorageStatus struct {
	ObservedStackdomeServerObjectGeneration int64 `json:"observedStackdomeServerObjectGeneration,omitempty"`
	// Conditions is a list of status conditions ths object is in.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Human readable status - please use .Conditions from code
	// +kubebuilder:default=Pending
	Phase StoragePhase `json:"phase,omitempty"`
	// +optional
	VolumeStatus []VolumeInfo `json:"volumeStatus,omitempty"`
	// +optional
	StatusHash string `json:"statusHash,omitempty"`
	// Name of the svc which exposes this storage pod(internally)
	// +optional
	ServiceName string `json:"serviceName"`
}

type VolumeInfo struct {
	VolumeName string `json:"volumeName"`
	// Path within the storage pod where the volume is mounted.
	Subpath   string `json:"subpath"`
	Available bool   `json:"Available"`
	// Applicable to only syncable volume types.
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// This Object is used to describe the storage requirements for a stackdome stack.
type Storage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StorageSpec   `json:"spec,omitempty"`
	Status StorageStatus `json:"status,omitempty"`
}

func (w *Storage) StatusHash() string {
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

func (w *Storage) HasSyncRequiredStorageResources() bool {
	for _, srs := range w.Spec.VolumeSpecs {
		if srs.NeedsSyncBeforeUse {
			return true
		}
	}
	return false
}

func (w *Storage) ContainsVolume(volumeName string) bool {
	_, found := w.Spec.VolumeSpecs[VolumeName(volumeName)]
	return found
}

func (w *Storage) MountPathForVolume(volumeName string) string {
	return fmt.Sprintf("/%s/%s", w.Name, volumeName)
}

func (w *Storage) VolumeInfo(volumeName string) *VolumeInfo {
	for i := range w.Status.VolumeStatus {
		if w.Status.VolumeStatus[i].VolumeName == volumeName {
			return &w.Status.VolumeStatus[i]
		}
	}
	return nil
}

func (w *Storage) VolumeSpecFor(volumeName string) *VolumeSpec {
	return w.Spec.VolumeSpecs[VolumeName(volumeName)]
}

//+kubebuilder:object:root=true

// WorkspaceStateList contains a list of WorkspaceState
type StorageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Storage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Storage{}, &StorageList{})
}
