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

	"github.com/davecgh/go-spew/spew"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

type BuildPhase string

const (
	BuildPhaseSuccess BuildPhase = "Success"
	BuildPhaseFailed  BuildPhase = "Failed"
	BuildPhasePending BuildPhase = "Pending"
)

type BuildStatusCondition string

const (
	BuildAvailable  BuildStatusCondition = "Available"
	BuildFailed     BuildStatusCondition = "Failed"
	BuildJobCreated BuildStatusCondition = "BuildJobCreated"
)

// ImageBuildSpec defines the desired state of a Build
type ImageBuildSpec struct {
	// Resource for which the image is being build.
	// +required
	ResourceName string `json:"resourceName"`
	// +required
	SourceRevision corev1alpha1.SourceRevisionSpec `json:"sourceRevision"`
	// Build context.
	// +required
	BuildContext BuildContextSpec `json:"buildContext"`
	// Registry details for pushing the built image
	// +required
	RegistryURL string `json:"registryUrl"`
	// Is registry insecure
	// +required
	InsecureRegistry bool `json:"insecureRegistry"`
	// This is populated by the WorkspaceResource controller before creating the build job.
	// +optional
	Auth *RegistryAuth `json:"auth,omitempty"`
}

// RegistryAuth contains the registry authentication details
type RegistryAuth struct {
	// Type of the registry authentication
	// +kubebuilder:validation:Enum=DockerHub;InClusterZotRegistry
	// +required
	Type corev1alpha1.RegistryAuthType `json:"type"`
	// DockerConfigAuth contains the Docker config authentication details
	// +optional
	DockerAuthSecretRef *DockerAuthSecretRef `json:"dockerAuthSecretRef,omitempty"`
}

// DockerAuthSecretRef references a Docker authentication secret
type DockerAuthSecretRef struct {
	// Name of the secret
	// +required
	SecretName string `json:"secretName"`

	// Namespace where the secret is located
	// +required
	SecretNamespace string `json:"secretNamespace"`

	// Key in the secret containing the Docker config
	// +required
	AuthKey string `json:"authKey"`
}

type BuildContextSpec struct {
	// Dockerfile path within the context source
	// Defaults to Dockerfile
	// +kubebuilder:default=Dockerfile
	// +optional
	DockerfilePath string `json:"dockerfilePath"`
	// Build Context path within the context source
	// Defaults to /
	// +kubebuilder:default=/
	// +optional
	ContextPath string `json:"contextPath"`
	// +required
	ContextSource *corev1alpha1.BuildContextSource `json:"contextSource"`
}

// ImageBuildStatus defines the observed state of ImageBuild
type ImageBuildStatus struct {
	// The most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions is a list of status conditions ths object is in.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// DEPRECATED: This field is not part of any API contract
	// it will go away as soon as kubectl can print conditions!
	// Human readable status - please use .Conditions from code
	// +kubebuilder:default=Pending
	Phase               BuildPhase `json:"phase,omitempty"`
	BuildSourceRevision string     `json:"buildSourceRevision,omitempty"`
	ImageUrl            string     `json:"imageUrl"`
	StatusHash          string     `json:"statusHash,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=imgbuild
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// ImageBuild is the Schema for the imagebuilds API
type ImageBuild struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ImageBuildSpec   `json:"spec,omitempty"`
	Status ImageBuildStatus `json:"status,omitempty"`
}

func (w *ImageBuild) StatusHash() string {
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

//+kubebuilder:object:root=true

// ImageBuildList contains a list of ImageBuild
type ImageBuildList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ImageBuild `json:"items"`
}

func ImageBuildName(resourceName string, srcRevision string) string {
	return fmt.Sprintf("%s-%s", resourceName, srcRevision[:7])
}

func (w *ImageBuild) ShortBuildSrcRevisionFromStatus() string {
	return w.Status.BuildSourceRevision[:7]
}

func (w *ImageBuild) ShortBuildSrcRevisionFromSpec() string {
	return w.Spec.SourceRevision.GetSourceRevisionString()[:7]
}

func init() {
	SchemeBuilder.Register(&ImageBuild{}, &ImageBuildList{})
}
