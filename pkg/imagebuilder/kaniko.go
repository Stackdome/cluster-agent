package imagebuilder

import (
	"bytes"
	"fmt"
	"path/filepath"
	"text/template"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
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
        image: gcr.io/kaniko-project/executor:latest
        args:
        - "--dockerfile={{ .DockerfilePath }}"
        - "--context=dir://{{ .Context }}"
        - "--destination={{ .Registry }}/{{ .ImageName }}:{{ .Tag }}"
        - "--insecure-registry={{ .Insecure }}"
        - "--insecure={{ .Insecure }}"
        - "--insecure-pull={{ .Insecure }}"
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

type BuildParams struct {
	JobName        string
	Namespace      string
	PVCName        string
	DockerfilePath string
	Context        string
	Registry       string
	ImageName      string
	Tag            string
	Insecure       bool
	UniqueVolumes  []string
	VolumeMounts   []VolumeMount
}

type VolumeMount struct {
	ContainerMountPath string
	PvcName            string
	SubPath            string
}

func (b *BuildParams) generateImageBuildJobYAML() (string, error) {
	tmpl, err := template.New("image-build-job").Parse(jobTemplate)
	if err != nil {
		return "", err
	}
	if len(b.VolumeMounts) == 0 {
		b.VolumeMounts = []VolumeMount{}
	}
	// Add default docker context for docker build.
	b.VolumeMounts = append(b.VolumeMounts, VolumeMount{
		ContainerMountPath: "/workspace",
		PvcName:            b.PVCName,
	})

	b.Context = filepath.Join("/workspace", b.Context)
	b.DockerfilePath = filepath.Join("/workspace", b.DockerfilePath)

	uniqueVolumes := []string{}
	uniqueVolumesMap := make(map[string]struct{})
	for _, mount := range b.VolumeMounts {
		_, added := uniqueVolumesMap[mount.PvcName]
		if !added {
			uniqueVolumes = append(uniqueVolumes, mount.PvcName)
			uniqueVolumesMap[mount.PvcName] = struct{}{}
		}
	}
	b.UniqueVolumes = uniqueVolumes
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, b)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}

func (b *BuildParams) ImageUrl() string {
	return fmt.Sprintf("%s/%s:%s", b.Registry, b.ImageName, b.Tag)
}

// TODO: Configurable resource requirements
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
	container.Resources = corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2000m"),
			corev1.ResourceMemory: resource.MustParse("8Gi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("200Mi"),
		},
	}

	container.Args = append(container.Args, "--cache=true", "--cache-copy-layers=true", "--cache-run-layers=true", "--cleanup=true")
	return job, nil
}
