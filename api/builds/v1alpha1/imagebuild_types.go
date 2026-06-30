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
	"crypto/sha256"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"

	"github.com/davecgh/go-spew/spew"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

const (
	AnnotationResourceName   = "builds.stackdome.io/resource-name"
	AnnotationSourceRevision = "builds.stackdome.io/source-revision"
	AnnotationDockerfilePath = "builds.stackdome.io/dockerfile-path"
)

type BuildPhase string

const (
	BuildPhaseSuccess   BuildPhase = "Success"
	BuildPhaseFailed    BuildPhase = "Failed"
	BuildPhasePending   BuildPhase = "Pending"
	BuildPhaseCancelled BuildPhase = "Cancelled"
)

type BuildStatusCondition string

const (
	BuildAvailable  BuildStatusCondition = "Available"
	BuildFailed     BuildStatusCondition = "Failed"
	BuildJobCreated BuildStatusCondition = "BuildJobCreated"
	BuildCancelled  BuildStatusCondition = "Cancelled"
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
	// Repository is the structured push target.
	// +required
	Repository corev1alpha1.ImageRepositorySpec `json:"repository"`
	// Build arguments passed to the Docker build as --build-arg flags.
	// +optional
	BuildArgs []corev1alpha1.BuildArg `json:"buildArgs,omitempty"`
	// When set to true, the build is cancelled and its build job will be deleted.
	// This field is immutable once set to true.
	// +optional
	// +kubebuilder:default=false
	// +kubebuilder:validation:XValidation:rule="!oldSelf || self",message="cancelled is immutable once set to true"
	Cancelled bool `json:"cancelled,omitempty"`
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
	Phase                  BuildPhase                      `json:"phase,omitempty"`
	BuildSourceRevision    string                          `json:"buildSourceRevision,omitempty"`
	ImageUrl               string                          `json:"imageUrl"`
	StatusHash             string                          `json:"statusHash,omitempty"`
	LastBuildFailureDetail *corev1alpha1.LastFailureDetail `json:"lastBuildFailureDetail,omitempty"`
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

var dnsLabelRegex = regexp.MustCompile(`[^a-z0-9]+`)

func SanitizeDNSLabel(s string, fallback string) string {
	s = strings.ToLower(s)
	s = dnsLabelRegex.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return fallback
	}
	return s
}

func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) > maxLen {
		return string(runes[:maxLen])
	}
	return s
}

func ImageBuildName(resourceName string, srcRevision string) string {
	cleanName := SanitizeDNSLabel(resourceName, "app")
	cleanRev := SanitizeDNSLabel(srcRevision, "rev")
	res := fmt.Sprintf("%s-%s", cleanName, cleanRev)
	if len(res) > 100 {
		res = res[:100]
		res = strings.TrimSuffix(res, "-")
	}
	return res
}

const (
	buildJobSuffix       = "-build"
	shortRevisionLen     = 8
	maxLabelValueLen     = 63
	maxResourceNameInJob = maxLabelValueLen - shortRevisionLen - len(buildJobSuffix) - 1 // 48
)

func BuildJobName(resourceName string, sourceRevision string) string {
	cleanName := SanitizeDNSLabel(resourceName, "app")

	if len(cleanName) > maxResourceNameInJob {
		cleanName = cleanName[:maxResourceNameInJob]
		cleanName = strings.TrimSuffix(cleanName, "-")
	}

	var cleanRev string
	if sourceRevision == "" {
		cleanRev = "rev"
	} else {
		hash := sha256.Sum256([]byte(sourceRevision))
		cleanRev = fmt.Sprintf("%x", hash)[:shortRevisionLen]
	}

	return fmt.Sprintf("%s-%s%s", cleanName, cleanRev, buildJobSuffix)
}

func (w *ImageBuild) ShortBuildSrcRevisionFromStatus() string {
	return truncateString(w.Status.BuildSourceRevision, 7)
}

func (w *ImageBuild) ShortBuildSrcRevisionFromSpec() string {
	return truncateString(w.Spec.SourceRevision.GetSourceRevisionString(), 7)
}

func init() {
	SchemeBuilder.Register(&ImageBuild{}, &ImageBuildList{})
}
