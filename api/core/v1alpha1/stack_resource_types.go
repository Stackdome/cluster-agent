package v1alpha1

import (
	"fmt"
	"hash/fnv"
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
	StackResourcePhasePending  StackResourcePhase = "Pending"
	StackResourcePhaseReady    StackResourcePhase = "Ready"
	StackResourcePhaseDegraded StackResourcePhase = "Degraded"
	StackResourcePhaseFailed   StackResourcePhase = "Failed"
)

type StackResourceStatusCondition string

const (
	StackResourceStatusAvailable   StackResourceStatusCondition = "Available"
	StackResourceTLSConfigured     StackResourceStatusCondition = "TLSConfigured"
	StackResourceDependenciesReady StackResourceStatusCondition = "DependenciesReady"
	StackResourceBuildReady        StackResourceStatusCondition = "BuildReady"
	StackResourcePreDeployComplete StackResourceStatusCondition = "PreDeployComplete"
	StackResourceWorkloadAvailable StackResourceStatusCondition = "WorkloadAvailable"
	StackResourceIngressReady      StackResourceStatusCondition = "IngressReady"
	StackResourceStalled           StackResourceStatusCondition = "Stalled"
	StackResourceConverged         StackResourceStatusCondition = "Converged"
)

type WorkloadType string

const (
	WorkloadTypeService         WorkloadType = "Service"
	WorkloadTypeStatefulService WorkloadType = "StatefulService"
	WorkloadTypeWorker          WorkloadType = "Worker"
	WorkloadTypeJob             WorkloadType = "Job"
	WorkloadTypeCronJob         WorkloadType = "CronJob"
)

const (
	RestartResourceAnnotation = "kubectl.kubernetes.io/restartedAt"
)

type HealthChecks struct {
	// +optional
	Readiness *Probe `json:"readiness,omitempty"`
	// +optional
	Liveness *Probe `json:"liveness,omitempty"`
	// +optional
	Startup *Probe `json:"startup,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="[has(self.httpGet), has(self.tcpSocket), has(self.command)].filter(x, x).size() == 1",message="exactly one of httpGet, tcpSocket, or command must be set"
type Probe struct {
	// +optional
	HTTPGet *HTTPGetProbe `json:"httpGet,omitempty"`
	// +optional
	TCPSocket *TCPSocketProbe `json:"tcpSocket,omitempty"`
	// +optional
	Command []string `json:"command,omitempty"`
	// +kubebuilder:default=0
	InitialDelaySeconds int32 `json:"initialDelaySeconds,omitempty"`
	// +kubebuilder:default=10
	PeriodSeconds int32 `json:"periodSeconds,omitempty"`
	// +kubebuilder:default=3
	FailureThreshold int32 `json:"failureThreshold,omitempty"`
	// +kubebuilder:default=1
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`
}

type HTTPGetProbe struct {
	// +kubebuilder:default=/
	Path string `json:"path,omitempty"`
	// PortName must reference a declared port name from spec.ports.
	// +required
	PortName string `json:"portName"`
}

type TCPSocketProbe struct {
	// PortName must reference a declared port name from spec.ports.
	// +required
	PortName string `json:"portName"`
}

type EnvVarSource struct {
	// +required
	SecretKeyRef corev1.SecretKeySelector `json:"secretKeyRef"`
}

// +kubebuilder:validation:XValidation:rule="has(self.buildSpec) != has(self.imageSpec)",message="exactly one of buildSpec or imageSpec must be set"
// +kubebuilder:validation:XValidation:rule="self.workloadType != 'CronJob' || (has(self.schedule) && size(self.schedule) > 0)",message="schedule is required for CronJob workloads"
// +kubebuilder:validation:XValidation:rule="self.workloadType == 'CronJob' || !has(self.schedule) || size(self.schedule) == 0",message="schedule is only valid for CronJob workloads"
// +kubebuilder:validation:XValidation:rule="!(self.workloadType in ['Worker','Job','CronJob']) || !has(self.ports) || size(self.ports) == 0",message="Worker, Job, and CronJob workloads cannot declare ports"
// +kubebuilder:validation:XValidation:rule="self.workloadType in ['Service','StatefulService'] || !has(self.preDeployCommand)",message="preDeployCommand is only valid for Service and StatefulService workloads"
// +kubebuilder:validation:XValidation:rule="self.workloadType in ['Service','StatefulService'] || !has(self.healthChecks)",message="healthChecks are only valid for Service and StatefulService workloads"
type StackResourceSpec struct {
	// +kubebuilder:validation:Enum=Service;StatefulService;Worker;Job;CronJob
	// +kubebuilder:default=Service
	WorkloadType WorkloadType `json:"workloadType,omitempty"`
	// +optional
	Schedule string `json:"schedule,omitempty"`
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// +optional
	BuildSpec *StackResourceBuildSpec `json:"buildSpec,omitempty"`
	// +optional
	ImageSpec *ImageSpec `json:"imageSpec,omitempty"`

	// +optional
	Init *InitSpec `json:"init,omitempty"`
	// +optional
	Command []string `json:"command,omitempty"`
	// +optional
	Args []string `json:"args,omitempty"`
	// +optional
	EnvironmentVariables []EnvironmentVariable `json:"environmentVariables,omitempty"`
	// +optional
	VolumeMounts []VolumeMount `json:"volumeMounts,omitempty"`
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
	// +optional
	Ports []Port `json:"ports,omitempty"`

	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	// +optional
	HealthChecks *HealthChecks `json:"healthChecks,omitempty"`
	// +optional
	PreDeployCommand []string `json:"preDeployCommand,omitempty"`
	// +kubebuilder:default=30
	// +optional
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`
	// +kubebuilder:default=false
	// +optional
	HardenedSecurityDefaults *bool `json:"hardenedSecurityDefaults,omitempty"`

	// +optional
	RestartRequest *metav1.Time `json:"restartRequest,omitempty"`
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

// +kubebuilder:validation:XValidation:rule="!self.exposeToPublic || size(self.fqdn) > 0",message="fqdn is required when exposeToPublic is true"
// +kubebuilder:validation:XValidation:rule="self.protocol == 'http' || !self.exposeToPublic",message="only http ports can be exposed to public"
type Port struct {
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	// +kubebuilder:validation:MaxLength=15
	Name string `json:"name"`
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Number int32 `json:"number"`
	// +kubebuilder:validation:Enum=http;grpc;tcp
	// +kubebuilder:default=http
	Protocol string `json:"protocol,omitempty"`
	// +kubebuilder:default=false
	ExposeToPublic bool `json:"exposeToPublic,omitempty"`
	// +optional
	FQDN string `json:"fqdn,omitempty"`
	// +optional
	TLS bool `json:"tls,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.value) != has(self.valueFrom)",message="exactly one of value or valueFrom must be set"
type EnvironmentVariable struct {
	// +required
	Name string `json:"name"`
	// +optional
	Value string `json:"value,omitempty"`
	// +optional
	ValueFrom *EnvVarSource `json:"valueFrom,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="has(self.value) != has(self.valueFrom)",message="exactly one of value or valueFrom must be set"
type BuildArg struct {
	// +required
	Name string `json:"name"`
	// +optional
	Value string `json:"value,omitempty"`
	// +optional
	ValueFrom *BuildArgValueSource `json:"valueFrom,omitempty"`
}

type BuildArgValueSource struct {
	// +required
	SecretKeyRef corev1.SecretKeySelector `json:"secretKeyRef"`
}

type VolumeMount struct {
	// +required
	SourceVolume string `json:"sourceVolume"`
	// +optional
	SourceSubPath string `json:"sourceSubPath,omitempty"`
	// +required
	Destination string `json:"destination"`
	// +kubebuilder:default=false
	ReadOnly bool `json:"readOnly,omitempty"`
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
	// Repository is the structured push target.
	// +required
	Repository ImageRepositorySpec `json:"repository"`
	// Build arguments passed to the Docker build as --build-arg flags.
	// +optional
	BuildArgs []BuildArg `json:"buildArgs,omitempty"`
}

// Current source revision for the build context.
// Can be :
// - volumeRevisionString of the source directory
// - git branch name + sha of the branch head
// - git tag name + sha of the tag
type SourceRevisionSpec struct {
	// +optional
	Volume *VolumeRevision `json:"volume,omitempty"`
	// +optional
	GitRepo *GitRepoRevision `json:"gitRepo,omitempty"`
}

func (s *SourceRevisionSpec) GetSourceRevisionString() string {
	switch {
	case s.Volume != nil:
		return s.Volume.RevisionString
	case s.GitRepo != nil:
		return s.GitRepo.GetGitRevisionString()
	}
	return ""
}

type VolumeRevision struct {
	// VolumeRevisionString is the string representation of the volume revision.
	// Can be:
	// - sha of the source directory (if using a synced volume)
	// - git branch name + sha of the branch head
	// - git tag name + sha of the tag
	// +required
	RevisionString string `json:"revisionString"`
}

// GitRepoRevision identifies a point-in-time in a git repository.
// Commit is the authoritative immutable identity (a full or abbreviated SHA).
// Branch or Tag is the fetchable ref used to clone; at least one must be set.
// +kubebuilder:validation:XValidation:rule="has(self.branch) || has(self.tag)",message="at least one of branch or tag must be set as a fetchable ref"
type GitRepoRevision struct {
	// +optional
	Branch string `json:"branch,omitempty"`
	// +optional
	Tag string `json:"tag,omitempty"`
	// +required
	// +kubebuilder:validation:MinLength=7
	// +kubebuilder:validation:MaxLength=40
	// +kubebuilder:validation:Pattern=`^[0-9a-f]+$`
	Commit string `json:"commit"`
}

func (s *GitRepoRevision) GetGitRevisionString() string {
	return s.Commit
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

type GitAuth struct {
	UsernamePasswordAuthRef *CredentialSecretKeyPair `json:"usernamePasswordAuthRef,omitempty"`
	PersonalAccessTokenRef  *CredentialSecretKeyPair `json:"personalAccessTokenRef,omitempty"`
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

// ImageRepositorySpec describes where a built image is pushed.
// Exactly one registry source must be set: clusterRegistryRef or external.
// +kubebuilder:validation:XValidation:rule="has(self.clusterRegistryRef) != has(self.external)",message="exactly one of clusterRegistryRef or external must be set"
type ImageRepositorySpec struct {
	// Cluster registry is cluster scoped.
	// +optional
	ClusterRegistryRef *corev1.LocalObjectReference `json:"clusterRegistryRef,omitempty"`
	// +optional
	External *ExternalRegistrySpec `json:"external,omitempty"`
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9._/-]*[a-z0-9]$|^[a-z0-9]$`
	Repository string `json:"repository"`
	// +optional
	TagPolicy *ImageTagPolicy `json:"tagPolicy,omitempty"`
	// +optional
	Auth *RegistryCredentialsSpec `json:"auth,omitempty"`
}

type ExternalRegistrySpec struct {
	// +required
	Host string `json:"host"`
	// +optional
	TLS *RegistryTLSSpec `json:"tls,omitempty"`
}

type RegistryTLSSpec struct {
	// +optional
	// +kubebuilder:default=false
	Insecure bool `json:"insecure,omitempty"`
}

type ImageTagPolicy struct {
	// +optional
	SourceRevision *SourceRevisionTagPolicy `json:"sourceRevision,omitempty"`
	// +optional
	Fixed *FixedTagPolicy `json:"fixed,omitempty"`
}

type SourceRevisionTagPolicy struct {
	// +optional
	// +kubebuilder:default=true
	Sanitize bool `json:"sanitize,omitempty"`
}

type FixedTagPolicy struct {
	// +required
	Tag string `json:"tag"`
}

type RegistryCredentialsSpec struct {
	// +optional
	DockerConfig *DockerConfigAuth `json:"dockerConfig,omitempty"`
	// +optional
	Basic *BasicAuthCredentials `json:"basic,omitempty"`
}

type BasicAuthCredentials struct {
	// +required
	SecretRef corev1.SecretReference `json:"secretRef"`
	// +required
	UsernameKey string `json:"usernameKey"`
	// +required
	PasswordKey string `json:"passwordKey"`
}

type ImageSpec struct {
	// Image reference (e.g., docker.io/myorg/myimage:tag)
	// +required
	Image string `json:"image"`
	// +optional
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
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
	ObservedGeneration int64                           `json:"observedGeneration,omitempty"`
	ObservedRevision   string                          `json:"observedRevision,omitempty"`
	LastConverged      *StackResourceConvergenceRecord `json:"lastConverged,omitempty"`
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
	CurrentBuild                  *BuildStatus        `json:"currentBuild,omitempty"`
	StatusHash                    string              `json:"statusHash,omitempty"`
	LastFailureDetails            []LastFailureDetail `json:"lastFailureDetail,omitempty"`
	LastFailureDeploymentRevision string              `json:"lastFailureDeploymentRevision,omitempty"`
	Replicas                      int32               `json:"replicas,omitempty"`
	AvailableReplicas             int32               `json:"availableReplicas,omitempty"`
	UpdatedReplicas               int32               `json:"updatedReplicas,omitempty"`
	LastRunTime                   *metav1.Time        `json:"lastRunTime,omitempty"`
	LastRunSucceeded              *bool               `json:"lastRunSucceeded,omitempty"`
}

type LastFailureDetail struct {
	ContainerName           string `json:"containerName,omitempty"`
	RestartCount            int32  `json:"restartCount,omitempty"`
	LastTerminationReason   string `json:"lastTerminationReason,omitempty"`
	LastTerminationMessage  string `json:"lastTerminationMessage,omitempty"`
	LastTerminationExitCode *int32 `json:"lastTerminationExitCode,omitempty"`
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
	if w.Spec.BuildSpec != nil && w.Spec.BuildSpec.Repository.Auth != nil {
		return true
	}
	return false
}

func (w *StackResource) SynthesizedDockerConfigSecretName() string {
	return SynthesizedDockerConfigSecretName(w.Name)
}

func SynthesizedDockerConfigSecretName(resourceName string) string {
	return resourceName + "-dockercfg"
}

func (w *StackResource) InitContainerName() string {
	return fmt.Sprintf("%s-init", w.Name)
}

func (w *StackResource) HasBuildSpec() bool {
	return w.Spec.BuildSpec != nil
}

func (w *StackResource) HasImageSpec() bool {
	return w.Spec.ImageSpec != nil
}

func (w *StackResource) StatusHash() string {
	hasher := fnv.New64a()
	hasher.Reset()
	printer := spew.ConfigState{
		Indent:         " ",
		SortKeys:       true,
		DisableMethods: true,
		SpewKeys:       true,
	}
	statusCopy := w.Status
	statusCopy.StatusHash = ""
	conds := make([]metav1.Condition, len(w.Status.Conditions))
	copy(conds, w.Status.Conditions)
	for i := range conds {
		conds[i].LastTransitionTime = metav1.Time{}
	}
	statusCopy.Conditions = conds
	printer.Fprintf(hasher, "%#v", statusCopy)
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum64()))
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
