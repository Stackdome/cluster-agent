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
	"net/url"
	"strings"

	"github.com/davecgh/go-spew/spew"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	common "stackdome.io/cluster-agent/api"
)

type RegistryAuthType string

const (
	RegistryAuthTypeDockerHub            RegistryAuthType = "DockerHub"
	RegistryAuthTypeInClusterZotRegistry RegistryAuthType = "InClusterZotRegistry"
)

type WorkspaceResourcePhase string

const (
	WorkspaceResourcePhasePending WorkspaceResourcePhase = "Pending"
	WorkspaceResourcePhaseReady   WorkspaceResourcePhase = "Ready"
	WorkspaceResourcePhaseFailed  WorkspaceResourcePhase = "Failed"
)

type WorkspaceResourceStatusCondition string

const (
	WorkspaceResourceStatusAvailable WorkspaceResourceStatusCondition = "Available"
)

const (
	RestartResourceAnnotation = "kubectl.kubernetes.io/restartedAt"
)

// WorkspaceResourceSpec defines the desired state of WorkspaceResource
type WorkspaceResourceSpec struct {
	// Only one of the following fields should be specified
	// +union
	BuildSpec *ResourceBuildSpec `json:"buildSpec,omitempty"`
	ImageSpec *ImageSpec         `json:"imageSpec,omitempty"`
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
	// +required
	Command []string `json:"command"`
	// +optional
	Args []string `json:"args"`
}

type WorkspaceStorageRef struct {
	WorkspaceStorageName string `json:"workspaceStorageName"`
}

type Port struct {
	Number int32 `json:"number"`
	// +optional
	// +kubebuilder:default=false
	ExposeToPublic bool `json:"exposeToPublic"`
	// +optional
	// +kubebuilder:default=true
	IsHttp bool `json:"isHttp"`
	// +required
	Subdomain string `json:"subdomain"`
}

type EnvironmentVariables struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type VolumeMount struct {
	SourceWorkspaceVolume string `json:"sourceWorkspaceVolume"`
	SourceSubPath         string `json:"sourceSubPath"`
	Destination           string `json:"destination"`
}

type ResourceBuildSpec struct {
	// Source volume where the build context is present.
	// +required
	SourceVolumeName string `json:"sourceVolumeName"`
	// Build Context within the source volume.
	// +required
	BuildContext string `json:"buildContext"`
	// Path to the docker file within the source volume.
	// +required
	DockerFilePath string `json:"dockerFilePath"`
	// +required
	BuildSourceHash string `json:"buildSourceHash"`
	// Registry specification for pushing the built image
	// +required
	Registry RegistrySpec `json:"registry"`
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
	Type             RegistryAuthType  `json:"type"`
	DockerConfigAuth *DockerConfigAuth `json:"dockerConfigAuth,omitempty"`
	// Add more config for other auth types like gcr, aws ecr etc.
}

type DockerConfigAuth struct {
	CredentialsRef *common.CredentialSecretKeyPair `json:"credentialsRef,omitempty"`
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
	Name       string `json:"name,omitempty"`
	SourceHash string `json:"sourceHash,omitempty"`
	ShortHash  string `json:"shortHash,omitempty"`
	Available  bool   `json:"available,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
	Phase      string `json:"phase,omitempty"`
}

// WorkspaceResourceStatus defines the observed state of WorkspaceResource
type WorkspaceResourceStatus struct {
	// The most recent generation observed by the controller.
	ObservedGeneration                      int64 `json:"observedGeneration,omitempty"`
	ObservedStackdomeServerObjectGeneration int64 `json:"observedStackdomeServerObjectGeneration,omitempty"`
	// Conditions is a list of status conditions ths object is in.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// DEPRECATED: This field is not part of any API contract
	// it will go away as soon as kubectl can print conditions!
	// Human readable status - please use .Conditions from code
	// +kubebuilder:default=Pending
	Phase           WorkspaceResourcePhase `json:"phase,omitempty"`
	ImageSourceHash string                 `json:"imageSourceHash,omitempty"`
	ExternalAddress []ExternalAddress      `json:"externalAddress,omitempty"`
	// Internal address is always the cluster wide resolvable internal domain name.
	InternalAddress               *string      `json:"internalAddress,omitempty"`
	LastRestartRequestProcessedAt *metav1.Time `json:"lastRestartRequestProcessedAt,omitempty"`
	// Current build that this resource uses.
	// Applicable only to resources which have ApplicationBuildSpec defined.
	CurrentBuild *BuildStatus `json:"currentBuild,omitempty"`
	StatusHash   string       `json:"statusHash,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=wr
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// WorkspaceResource is the Schema for the workspaceresources API
type WorkspaceResource struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceResourceSpec   `json:"spec,omitempty"`
	Status WorkspaceResourceStatus `json:"status,omitempty"`
}

func (w *WorkspaceResource) NeedsPullSecret() bool {
	if w.Spec.ImageSpec != nil && w.Spec.ImageSpec.PullAuth != nil {
		return true
	}
	if w.Spec.BuildSpec != nil && w.Spec.BuildSpec.Registry.Auth != nil {
		return true
	}
	return false
}

func (w *WorkspaceResource) RegistryAuthUrl() (string, error) {
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

func (w *WorkspaceResource) RegistryAuthType() RegistryAuthType {
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

func (w *WorkspaceResource) StatusHash() string {
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
// WorkspaceResourceList contains a list of WorkspaceResource
type WorkspaceResourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkspaceResource `json:"items"`
}

func (w *WorkspaceResourceSpec) HasExposedPort() bool {
	for _, port := range w.Ports {
		if port.ExposeToPublic {
			return true
		}
	}
	return false
}

func (w *WorkspaceResource) SplitPortsByInternalAndExternal() ([]Port, []Port) {
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

func (w *WorkspaceResource) VolumeMountSources() []string {
	res := make([]string, 0)
	for _, volumeMount := range w.Spec.VolumeMounts {
		res = append(res, volumeMount.SourceWorkspaceVolume)
	}
	return res
}

func init() {
	SchemeBuilder.Register(&WorkspaceResource{}, &WorkspaceResourceList{})
}
