package v1alpha1

import (
	"fmt"
	"hash/fnv"
	"net/url"
	"strings"

	"github.com/davecgh/go-spew/spew"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
)

type RegistryAuthType string

const (
	RegistryAuthTypeDockerHub            RegistryAuthType = "DockerHub"
	RegistryAuthTypeInClusterZotRegistry RegistryAuthType = "InClusterZotRegistry"
)

type StackResourcePhase string

const (
	StackResourcePhasePending StackResourcePhase = "Pending"
	StackResourcePhaseReady   StackResourcePhase = "Ready"
	StackResourcePhaseFailed  StackResourcePhase = "Failed"
)

type StackResourceStatusCondition string

const (
	StackResourceStatusAvailable StackResourceStatusCondition = "Available"
)

const (
	RestartResourceAnnotation = "kubectl.kubernetes.io/restartedAt"
)

// StackResourceSpec defines the desired state of a StackResource
type StackResourceSpec struct {
	// Only one of the following fields should be specified
	// +union
	BuildSpec *StackResourceBuildSpec `json:"buildSpec,omitempty"`
	ImageSpec *ImageSpec              `json:"imageSpec,omitempty"`
	// +optional
	Init *InitSpec `json:"init,omitempty"`
	// +optional
	Command []string `json:"command"`
	// +optional
	Args []string `json:"args"`
	// +optional
	VolumeMounts []VolumeMount `json:"volumeMounts,omitempty"`
	// +optional
	EnvironmentVariables []EnvironmentVariables `json:"environmentVariables,omitempty"`
	// Other resources this workspace resource depends on.
	// +optional
	DependsOn []string `json:"dependsOn"`
	// +optional
	Ports []Port `json:"ports"`
	// +optional
	RestartRequest *metav1.Time `json:"restartRequest,omitempty"`
	// +optional
	StateFul bool `json:"stateFul"`
}

type InitSpec struct {
	// +optional
	Command []string `json:"command"`
	// +optional
	Args []string `json:"args"`
	// If the init step should run a different image.
	// +optional
	ImageSpec *ImageSpec `json:"imageSpec"`
}

// type WorkspaceStorageRef struct {
// 	WorkspaceStorageName string `json:"workspaceStorageName"`
// }

type Port struct {
	// +required
	Number int32 `json:"number"`
	// +optional
	// +kubebuilder:default=false
	ExposeToPublic bool `json:"exposeToPublic"`
	// +optional
	// +kubebuilder:default=true
	IsHttp bool `json:"isHttp"`
	// +required
	FQDN string `json:"fqdn"`
}

type EnvironmentVariables struct {
	// +required
	Name string `json:"name"`
	// +required
	Value string `json:"value"`
}

type VolumeMount struct {
	// +required
	SourceVolume string `json:"sourceVolume"`
	// +optional
	SourceSubPath string `json:"sourceSubPath"`
	// +required
	Destination string `json:"destination"`
}

type StackResourceBuildSpec struct {
	// Source context where the build context is present.
	// +required
	SourceContext BuildContextSource `json:"sourceContext"`
	// Build Context within the context source.
	// +required
	BuildContext string `json:"buildContext"`
	// Path to the docker file within the source volume.
	// +required
	DockerFilePath string `json:"dockerFilePath"`
	// Current source revision for the build context.
	// +required
	SourceRevision SourceRevisionSpec `json:"sourceRevision"`
	// Registry specification for pushing the built image.
	// +required
	Registry RegistrySpec `json:"registry"`
}

// Current source revision for the build context.
// Can be :
// - git commit hash
// - git branch name + sha of the branch head
// - git tag name
// - sha of the source directory (if using a synced volume)
type SourceRevisionSpec struct {
	// +optional
	Volume *VolumeRevision `json:"volume,omitempty"`
	// +optional
	GitRepo *GitRepoRevision `json:"gitRepo,omitempty"`
}

func (s *SourceRevisionSpec) GetSourceRevisionString() string {
	switch {
	case s.Volume != nil:
		return s.Volume.CurrentVolumeHash
	case s.GitRepo != nil:
		return s.GitRepo.GetGitRevisionString()
	}
	return ""
}

type VolumeRevision struct {
	// Hash of the contents of the volume.
	// +required
	CurrentVolumeHash string `json:"currentVolumeHash"`
}

// Can be one of the following:
// - git commit hash
// - git branch name
// - git tag name
type GitRepoRevision struct {
	// +optional
	Branch *GitBranch `json:"branch"`
	// +optional
	Tag string `json:"tag"`
	// +optional
	Commit string `json:"commit"`
}

func (s *GitRepoRevision) GetGitRevisionString() string {
	if s.Commit != "" {
		return s.Commit
	}
	if s.Tag != "" {
		return s.Tag
	}
	if s.Branch != nil {
		return fmt.Sprintf("%s-%s", strings.ToLower(s.Branch.Name), strings.ToLower(s.Branch.HeadSha))
	}
	return ""
}

type BuildContextSource struct {
	// +optional
	Volume *VolumeSource `json:"volume"`
	// +optional
	Git *GitRepoSource `json:"git"`
}

type VolumeSource struct {
	// +required
	Name string `json:"name"`
}

type GitRepoSource struct {
	// +required
	RepoUrl string `json:"repoUrl"`
	// +optional
	Auth *GitAuth `json:"auth"`
}

type GitBranch struct {
	// Name of the branch
	// +required
	Name string `json:"name"`
	// Current commit hash of the branch head.
	// default is head
	// +kubebuilder:default=HEAD
	// +required
	HeadSha string `json:"HeadSha"`
}

type GitAuth struct {
	UsernamePasswordAuthRef *CredentialSecretKeyPair `json:"usernamePasswordAuthRef,omitempty"`
	PersonalAccessTokenRef  *CredentialSecretKeyPair `json:"personalAccessTokenRef,omitempty"`
}

type RegistrySpec struct {
	// Repository URL for constructing the image tag (e.g., docker.io/myorg)
	// +required
	RepositoryURL string `json:"repositoryUrl"`
	// Is the registry insecure
	// +optional
	// +kubebuilder:default=false
	Insecure bool `json:"insecure"`
	// +optional
	Auth *RegistryAuth `json:"auth"`
}

type RegistryAuth struct {
	// +required
	Type RegistryAuthType `json:"type"`
	// +required
	DockerConfigAuth *DockerConfigAuth `json:"dockerConfigAuth,omitempty"`
	// Add more config for other auth types like gcr, aws ecr etc.
}

type DockerConfigAuth struct {
	// Key inside the secret that contains the Docker config JSON
	// +required
	SecretKey string `json:"secretKey"`
	// Reference to the secret containing the Docker config JSONs
	// +required
	SecretRef *corev1.SecretReference `json:"secretRef"`
}

func (r *RegistryAuth) GetDockerConfigSecretKey() string {
	return ".dockerconfigjson"
}

func (r *RegistryAuth) GetAuthURL(registryHost string) string {
	switch r.Type {
	case RegistryAuthTypeDockerHub:
		return "https://index.docker.io/v1/"
	default:
		if strings.HasPrefix(registryHost, "https://") || strings.HasPrefix(registryHost, "http://") {
			return registryHost
		}
		return fmt.Sprintf("http://%s", registryHost)
	}
}

type ImageSpec struct {
	// Image reference (e.g., docker.io/myorg/myimage:tag)
	// +required
	Image string `json:"image"`
	// Registry authentication for pulling the image
	// +optional
	PullAuth *RegistryAuth `json:"pullAuth,omitempty"`
}

type ExternalAddress struct {
	TargetPort int32  `json:"targetPort"`
	Address    string `json:"address"`
}

type BuildStatus struct {
	Name           string `json:"name,omitempty"`
	SourceRevision string `json:"sourceRevision,omitempty"`
	Available      bool   `json:"available,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Message        string `json:"message,omitempty"`
	Phase          string `json:"phase,omitempty"`
}

// StackResourceStatus defines the observed state of StackResource
type StackResourceStatus struct {
	// The most recent generation observed by the controller.
	ObservedGeneration                      int64 `json:"observedGeneration,omitempty"`
	ObservedStackdomeServerObjectGeneration int64 `json:"observedStackdomeServerObjectGeneration,omitempty"`
	// Conditions is a list of status conditions ths object is in.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// +kubebuilder:default=Pending
	Phase               StackResourcePhase `json:"phase,omitempty"`
	ImageSourceRevision string             `json:"imageSourceRevision,omitempty"`
	ExternalAddress     []ExternalAddress  `json:"externalAddress,omitempty"`
	// Internal address is always the cluster wide resolvable internal domain name.
	InternalAddress               *string      `json:"internalAddress,omitempty"`
	LastRestartRequestProcessedAt *metav1.Time `json:"lastRestartRequestProcessedAt,omitempty"`
	// Current build that this resource uses.
	// Applicable only to resources which have BuildSpec defined.
	CurrentBuild *BuildStatus `json:"currentBuild,omitempty"`
	StatusHash   string       `json:"statusHash,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sr
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// StackResource is the Schema for the StackResources API
type StackResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   StackResourceSpec   `json:"spec,omitempty"`
	Status StackResourceStatus `json:"status,omitempty"`
}

func (w *StackResource) NeedsPullSecret() bool {
	if w.Spec.ImageSpec != nil && w.Spec.ImageSpec.PullAuth != nil {
		return true
	}
	if w.Spec.BuildSpec != nil && w.Spec.BuildSpec.Registry.Auth != nil {
		return true
	}
	return false
}

func (w *StackResource) RegistryAuthUrl() (string, error) {
	var registryHost string
	var err error
	if w.Spec.ImageSpec != nil {
		registryHost, err = getHostFromURL(w.Spec.ImageSpec.Image)
		if err != nil {
			return "", err
		}
	} else {
		registryHost, err = getHostFromURL(w.Spec.BuildSpec.Registry.RepositoryURL)
		if err != nil {
			return "", err
		}
	}

	if w.Spec.ImageSpec != nil && w.Spec.ImageSpec.PullAuth != nil {
		return w.Spec.ImageSpec.PullAuth.GetAuthURL(registryHost), nil
	}
	if w.Spec.BuildSpec != nil && w.Spec.BuildSpec.Registry.Auth != nil {
		return w.Spec.BuildSpec.Registry.Auth.GetAuthURL(registryHost), nil
	}
	return "", fmt.Errorf("missing registry auth url")
}

func (w *StackResource) HasBuildSpec() bool {
	return w.Spec.BuildSpec != nil
}

func (w *StackResource) HasImageSpec() bool {
	return w.Spec.ImageSpec != nil
}

func (w *StackResource) RegistryAuthType() RegistryAuthType {
	if w.Spec.ImageSpec != nil && w.Spec.ImageSpec.PullAuth != nil {
		return w.Spec.ImageSpec.PullAuth.Type
	}
	if w.Spec.BuildSpec != nil && w.Spec.BuildSpec.Registry.Auth != nil {
		return w.Spec.BuildSpec.Registry.Auth.Type
	}
	return ""
}

func getHostFromURL(urlString string) (string, error) {
	// Handle URLs that might not have a scheme
	if !strings.HasPrefix(urlString, "http://") && !strings.HasPrefix(urlString, "https://") {
		urlString = "http://" + urlString
	}

	parsedURL, err := url.Parse(urlString)
	if err != nil {
		return "", err
	}

	return parsedURL.Host, nil
}

func (w *StackResource) StatusHash() string {
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
// StackResourceList contains a list of StackResource
type StackResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StackResource `json:"items"`
}

func (w *StackResourceSpec) HasExposedPort() bool {
	for _, port := range w.Ports {
		if port.ExposeToPublic {
			return true
		}
	}
	return false
}

func (w *StackResource) SplitPortsByInternalAndExternal() ([]Port, []Port) {
	internalPorts := make([]Port, 0)
	externalPorts := make([]Port, 0)
	for _, port := range w.Spec.Ports {
		if port.ExposeToPublic {
			externalPorts = append(externalPorts, port)
		} else {
			internalPorts = append(internalPorts, port)
		}
	}
	return internalPorts, externalPorts
}

func (w *StackResource) VolumeMountSources() []string {
	res := make([]string, 0)
	for _, volumeMount := range w.Spec.VolumeMounts {
		res = append(res, volumeMount.SourceVolume)
	}
	return res
}

func init() {
	SchemeBuilder.Register(&StackResource{}, &StackResourceList{})
}
