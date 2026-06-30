package v1alpha1

import (
	"fmt"
	"hash/fnv"

	corev1 "k8s.io/api/core/v1"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"

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
	VolumeConditionAvailable           VolumeCondition = "Available"
	VolumeConditionSyncedFromRemote    VolumeCondition = "SyncedFromRemote"
	VolumeConditionSyncedFromGitSource VolumeCondition = "VolumeSyncedFromGitSource"
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
	RemoteDir *RemoteDirSource `json:"remoteDir,omitempty"`
	// +optional
	BuildArtifacts []BuildArtifactSource `json:"buildArtifacts,omitempty"`
	// +optional
	GitRepo *GitRepoSource `json:"gitRepo,omitempty"`
}

type GitRepoSource struct {
	// +required
	RepoUrl string `json:"repoUrl"`
	// +optional
	Revision corev1alpha1.GitRepoRevision `json:"revision,omitempty"`
	// Destination within the volume where the git repo should be synced
	// +required
	// +kubebuilder:default=repo
	DestinationWithinVolume string `json:"destinationWithinVolume"`
	// +optional
	Auth *GitAuth `json:"auth"`
}

type GitAuth struct {
	UsernamePasswordAuthRef *corev1alpha1.CredentialSecretKeyPair `json:"usernamePasswordAuthRef,omitempty"`
	PersonalAccessTokenRef  *corev1alpha1.CredentialSecretKeyPair `json:"personalAccessTokenRef,omitempty"`
}

type RemoteDirSource struct {
	// Path within the client where the directory to be synced is located.
	// +required
	Path string `json:"path"`
	// We use this to track the current state of the directory
	// +required
	// +kubebuilder:validation:MinLength=1
	CurrentDirectoryHash string `json:"currentDirectoryHash,omitempty"`
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
	Phase VolumePhase `json:"phase,omitempty"`
	// +optional      //map(resourceRef => BuildArtifactSyncInfo)
	BuildArtifactSyncs map[string]BuildArtifactSyncInfo `json:"buildArtifactSyncs,omitempty"`
	// Used to track the status of the volume. This is a hash of the entire status block.
	// This is used by the stackdome api server to determine if the status has changed since
	// it last observed it.
	StatusHash string `json:"statusHash,omitempty"`
	// Can be
	// - a commit hash
	// - a branch name
	// - a tag name
	// +optional
	LastSyncedGitReference string `json:"lastSyncedGitReference,omitempty"`
	// Tracks the last time the volume was synced from a remote g.
	// +optional
	LastRemoteSyncHash string `json:"lastRemoteSyncHash,omitempty"`

	// +optional
	// The path within the volume where the git repo was synced to.
	GitRepoSyncedPathWithinVolume string `json:"gitRepoSyncedPathWithinVolume,omitempty"`
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

func (w *Volume) MarkAsSynced(clientDirectoryHash string) {
	if w.Spec.Source != nil && w.Spec.Source.RemoteDir != nil {
		w.Spec.Source.RemoteDir.CurrentDirectoryHash = clientDirectoryHash
	}
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

func (s *VolumeStatus) SetGitSourceSyncStatus(stackResourceRef StackResourceReference, buildID string, status BuildArtifactSyncStatus) {
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
