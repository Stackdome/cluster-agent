package imagebuilder

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	"stackdome.io/cluster-agent/pkg/config"
)

const jobTemplate = `
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ .JobName }}
  namespace: {{ .Namespace }}
spec:
  template:
    spec:
      containers:
      - name: kaniko
        image: kaniko-image
        args:
        {{- if .IsGitSource }}
        - "--dockerfile={{ .DockerfilePath }}"
        - "--context={{ .GitContextUrl }}"
        - "--destination={{ .RegistryURL }}/{{ .ImageName }}:{{ .Tag }}"
        - "--insecure-registry={{ .Insecure }}"
        - "--insecure={{ .Insecure }}"
        - "--insecure-pull={{ .Insecure }}"
        {{- else }}
        - "--dockerfile={{ .DockerfilePath }}"
        - "--context=dir://{{ .Context }}"
        - "--destination={{ .RegistryURL }}/{{ .ImageName }}:{{ .Tag }}"
        - "--insecure-registry={{ .Insecure }}"
        - "--insecure={{ .Insecure }}"
        - "--insecure-pull={{ .Insecure }}"
        {{- end }}
        {{- if .GitAuth }}
        env:
        - name: GIT_USERNAME
          valueFrom:
            secretKeyRef:
              name: {{ .GitSecretName }}
              key: {{ .GitUsernameKey }}
        - name: GIT_PASSWORD
          valueFrom:
            secretKeyRef:
              name: {{ .GitSecretName }}
              key: {{ .GitPasswordKey }}
        {{- end }}
        volumeMounts:
        {{- range .VolumeMounts }}
        - name: {{ .PvcName }}
          mountPath: {{ .ContainerMountPath }}
          {{- if ne (len .SubPath) 0  }}	
          subPath: {{ .SubPath }}
          {{- end }}
        {{- end }}
      restartPolicy: OnFailure
      volumes:
      {{- range .UniqueVolumes }}
      - name: {{ . }}
        persistentVolumeClaim:
          claimName: {{ . }}
      {{- end }}
`

// BuildParamsBuilder implements the builder pattern for BuildParams
type buildParamsBuilder struct {
	params BuildParams
}

// NewBuildParamsBuilder creates a new builder for BuildParams
func NewBuildParamsBuilder() *buildParamsBuilder {
	return &buildParamsBuilder{
		params: BuildParams{
			// Set default values here if needed
			Insecure:      false,
			VolumeMounts:  []VolumeMount{},
			UniqueVolumes: []string{},
		},
	}
}

// WithJobName sets the job name
func (b *buildParamsBuilder) WithJobName(name string) *buildParamsBuilder {
	b.params.JobName = name
	return b
}

// WithNamespace sets the namespace
func (b *buildParamsBuilder) WithNamespace(namespace string) *buildParamsBuilder {
	b.params.Namespace = namespace
	return b
}

// WithDockerfilePath sets the Dockerfile path
func (b *buildParamsBuilder) WithDockerfilePath(path string) *buildParamsBuilder {
	b.params.DockerfilePath = path
	return b
}

// WithContextPath sets the context path
func (b *buildParamsBuilder) WithContextPath(path string) *buildParamsBuilder {
	b.params.Context = path
	return b
}

// WithRegistryURL sets the registry URL
func (b *buildParamsBuilder) WithRegistryURL(url string) *buildParamsBuilder {
	b.params.RegistryURL = url
	return b
}

// WithImageName sets the image name
func (b *buildParamsBuilder) WithImageName(name string) *buildParamsBuilder {
	b.params.ImageName = name
	return b
}

// WithTag sets the image tag
func (b *buildParamsBuilder) WithTag(tag string) *buildParamsBuilder {
	b.params.Tag = tag
	return b
}

// WithInsecureRegistry sets whether the registry is insecure
func (b *buildParamsBuilder) WithInsecureRegistry(insecure bool) *buildParamsBuilder {
	b.params.Insecure = insecure
	return b
}

// WithSource sets the build source
func (b *buildParamsBuilder) WithSource(source *Source) *buildParamsBuilder {
	b.params.Source = source
	return b
}

// WithDockerAuth sets Docker registry authentication
func (b *buildParamsBuilder) WithDockerAuth(secret *corev1.Secret, secretKey string) *buildParamsBuilder {
	b.params.DockerConfigSecret = secret
	b.params.DockerConfigSecretKey = secretKey
	return b
}

// WithBuildArgs sets the resolved build arguments
func (b *buildParamsBuilder) WithBuildArgs(args []ResolvedBuildArg) *buildParamsBuilder {
	b.params.BuildArgs = args
	return b
}

// Build creates the final BuildParams object
func (b *buildParamsBuilder) Build() BuildParams {
	return b.params
}

type BuildParams struct {
	JobName               string
	Namespace             string
	DockerfilePath        string
	Context               string
	RegistryURL           string
	DockerConfigSecret    *corev1.Secret
	DockerConfigSecretKey string
	ImageName             string
	Tag                   string
	Insecure              bool
	UniqueVolumes         []string
	VolumeMounts          []VolumeMount
	Source                *Source
	// Git-specific fields
	IsGitSource    bool
	GitContextUrl  string
	GitAuth        bool
	GitSecretName  string
	GitUsernameKey string
	GitPasswordKey string
	BuildArgs      []ResolvedBuildArg
}

type Source struct {
	Volume  *VolumeSource
	GitRepo *GitRepoBuildSource
}

type GitRepoBuildSource struct {
	Repo     *corev1alpha1.GitRepoSource
	Revision *corev1alpha1.GitRepoRevision
}

type VolumeSource struct {
	PvcName string
	// Used only when the build volume is populated by git sync.
	GitRepoPath string
}

type DockerAuthSecretRef struct {
	SecretName      string
	SecretNamespace string
	AuthKey         string
}

type VolumeMount struct {
	ContainerMountPath string
	PvcName            string
	SubPath            string
}

type ResolvedBuildArg struct {
	Name  string
	Value string
}

// Converts GitRepoSource to a Kaniko Git context URL
func buildGitContextUrl(repo *corev1alpha1.GitRepoSource, revision *corev1alpha1.GitRepoRevision) string {
	// Convert https:// URLs to git:// format
	gitUrl := repo.RepoUrl
	if strings.HasPrefix(gitUrl, "https://") {
		gitUrl = "git://" + strings.TrimPrefix(gitUrl, "https://")
	} else if !strings.HasPrefix(gitUrl, "git://") {
		gitUrl = "git://" + gitUrl
	}

	// Add reference if specified
	if revision.Branch != nil && len(revision.Branch.Name) > 0 {
		gitUrl += fmt.Sprintf("#refs/heads/%s", revision.Branch.Name)
	} else if revision.Tag != "" {
		gitUrl += fmt.Sprintf("#refs/tags/%s", revision.Tag)
	}

	// Add commit if specified
	if revision.Commit != "" {
		// If we already have a branch/tag reference, add the commit
		if strings.Contains(gitUrl, "#") {
			gitUrl += fmt.Sprintf("#%s", revision.Commit)
		} else {
			// If no branch/tag, just add the commit
			gitUrl += fmt.Sprintf("#%s", revision.Commit)
		}
	}

	return gitUrl
}

func (b *BuildParams) generateImageBuildJobYAML() (string, error) {
	tmpl, err := template.New("image-build-job").Parse(jobTemplate)
	if err != nil {
		return "", err
	}

	if len(b.VolumeMounts) == 0 {
		b.VolumeMounts = []VolumeMount{}
	}

	// Setup based on source type
	if b.Source != nil {
		if b.Source.Volume != nil {
			// Handle Volume source
			b.IsGitSource = false
			contextMount := VolumeMount{
				ContainerMountPath: "/workspace",
				PvcName:            b.Source.Volume.PvcName,
			}
			// If GitRepoPath is present use that as the subpath within the pv.
			if len(b.Source.Volume.GitRepoPath) != 0 {
				contextMount.SubPath = b.Source.Volume.GitRepoPath
			}
			b.VolumeMounts = append(b.VolumeMounts, contextMount)
			b.Context = filepath.Join("/workspace", b.Context)
			b.DockerfilePath = filepath.Join("/workspace", b.DockerfilePath)
		} else if b.Source.GitRepo != nil {
			// Handle Git repo source
			b.IsGitSource = true
			b.GitContextUrl = buildGitContextUrl(b.Source.GitRepo.Repo, b.Source.GitRepo.Revision)

			// Setup Git auth if provided
			if b.Source.GitRepo.Repo.Auth != nil {
				b.GitAuth = true

				// Configure auth based on provided credentials
				if b.Source.GitRepo.Repo.Auth.UsernamePasswordAuthRef != nil {
					creds := b.Source.GitRepo.Repo.Auth.UsernamePasswordAuthRef
					b.GitSecretName = creds.SecretRef.Name
					b.GitUsernameKey = creds.UsernameKey
					b.GitPasswordKey = creds.PasswordKey
				} else if b.Source.GitRepo.Repo.Auth.PersonalAccessTokenRef != nil {
					creds := b.Source.GitRepo.Repo.Auth.PersonalAccessTokenRef
					b.GitSecretName = creds.SecretRef.Name
					b.GitUsernameKey = creds.UsernameKey
					b.GitPasswordKey = creds.PasswordKey
				}
			}

			// For Git sources, the dockerfile path and context are relative to the repo root
			// No need to adjust paths as they are already relative to the repo root
		}
	}

	// Generate unique volumes list
	uniqueVolumesMap := make(map[string]struct{})
	for _, existingVolume := range b.UniqueVolumes {
		uniqueVolumesMap[existingVolume] = struct{}{}
	}

	for _, mount := range b.VolumeMounts {
		_, added := uniqueVolumesMap[mount.PvcName]
		if !added {
			b.UniqueVolumes = append(b.UniqueVolumes, mount.PvcName)
			uniqueVolumesMap[mount.PvcName] = struct{}{}
		}
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, b)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}

func (b *BuildParams) ImageUrl() string {
	return fmt.Sprintf("%s/%s:%s", b.RegistryURL, b.ImageName, b.Tag)
}

// GenerateImageBuildJob creates a Kubernetes Job to build an image using Kaniko
func GenerateImageBuildJob(params BuildParams) (*batchv1.Job, error) {
	jobYaml, err := params.generateImageBuildJobYAML()
	if err != nil {
		return nil, err
	}

	job := &batchv1.Job{}
	if err := yaml.Unmarshal([]byte(jobYaml), job); err != nil {
		return nil, fmt.Errorf("failed to decode Job YAML: %v", err)
	}

	container := &job.Spec.Template.Spec.Containers[0]

	// Set kaniko image
	container.Image = config.KanikoExecutorImage

	container.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("200Mi"),
		},
	}

	// Configure Docker registry authentication
	configureAuth(job, params)

	// 30 minutes TTL after job finished
	job.Spec.TTLSecondsAfterFinished = ptr.To(int32(60 * 30))

	// Add common Kaniko args for all builds
	container.Args = append(container.Args, "--cache=true", "--cache-copy-layers=true", "--cache-run-layers=true", "--cleanup=true")

	for _, arg := range params.BuildArgs {
		container.Args = append(container.Args, fmt.Sprintf("--build-arg=%s=%s", arg.Name, arg.Value))
	}

	return job, nil
}

func configureAuth(job *batchv1.Job, params BuildParams) {
	container := &job.Spec.Template.Spec.Containers[0]

	// Configure Docker registry authentication
	if params.DockerConfigSecret != nil {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "dockerconfig",
			ReadOnly:  true,
			MountPath: "/kaniko/.docker",
		})
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "dockerconfig",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: params.DockerConfigSecret.Name,
					Items: []corev1.KeyToPath{
						{
							Key:  params.DockerConfigSecretKey,
							Path: "config.json",
						},
					},
				},
			},
		})
	}
}

// Helper function to create BuildParams
func CreateBuildParams(
	jobName string,
	namespace string,
	registryURL string,
	imageName string,
	tag string,
	insecure bool,
	dockerfilePath string,
	context string,
	source *Source,
	dockerSecret *corev1.Secret,
	dockerSecretKey string,
) BuildParams {
	return BuildParams{
		JobName:               jobName,
		Namespace:             namespace,
		DockerfilePath:        dockerfilePath,
		Context:               context,
		RegistryURL:           registryURL,
		ImageName:             imageName,
		Tag:                   tag,
		Insecure:              insecure,
		Source:                source,
		DockerConfigSecret:    dockerSecret,
		DockerConfigSecretKey: dockerSecretKey,
	}
}
