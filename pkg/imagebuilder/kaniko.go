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
        {{- else }}
        - "--dockerfile={{ .DockerfilePath }}"
        - "--context=dir://{{ .Context }}"
        {{- end }}
        - "--destination={{ .ImageUrl }}"
        {{- if .Insecure }}
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

// WithDestination sets the full OCI destination reference
func (b *buildParamsBuilder) WithDestination(ref string) *buildParamsBuilder {
	b.params.Destination = ref
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
	Destination           string
	DockerConfigSecret    *corev1.Secret
	DockerConfigSecretKey string
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
	gitUrl := repo.RepoUrl
	if strings.HasPrefix(gitUrl, "https://") {
		gitUrl = "git://" + strings.TrimPrefix(gitUrl, "https://")
	} else if !strings.HasPrefix(gitUrl, "git://") {
		gitUrl = "git://" + gitUrl
	}

	switch {
	case revision.Branch != "":
		gitUrl += fmt.Sprintf("#refs/heads/%s", revision.Branch)
	case revision.Tag != "":
		gitUrl += fmt.Sprintf("#refs/tags/%s", revision.Tag)
	}

	if revision.Commit != "" {
		gitUrl += fmt.Sprintf("#%s", revision.Commit)
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
	return b.Destination
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

	container.Image = config.KanikoExecutorImage
	container.TerminationMessagePolicy = corev1.TerminationMessageFallbackToLogsOnError

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
	// Set backoff limit to 3 to retry failed builds up to 3 times.
	job.Spec.BackoffLimit = ptr.To(int32(3))

	// Add common Kaniko args for all builds
	// --ignore-path=/product_uuid avoids "device or resource busy" errors when
	// running inside Kind clusters, where that path is a host-mounted virtual file.
	container.Args = append(container.Args, "--cache=true", "--cache-copy-layers=true", "--cache-run-layers=true", "--cleanup=true", "--ignore-path=/product_uuid")

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
