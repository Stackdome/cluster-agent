package imagebuilder

import (
	"bytes"
	"fmt"
	"path/filepath"
	"text/template"

	batchv1 "k8s.io/api/batch/v1"
	"sigs.k8s.io/yaml"
	"soradev.io/cluster-agent/api/v1alpha1"
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
        - "--insecure-registry=true"
        - "--insecure=true"
        - "--insecure-pull=true"
        - "--skip-push-permission-check=true"
        volumeMounts:
        {{- range .VolumeMounts }}
        - name: {{ .PvcName }}
          mountPath: {{ .ContainerMountPath }}
          {{- if .SubPath }}
          subPath: {{ .SubPath }}
          {{- end }}
        {{- end }}
      restartPolicy: Never
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
	VolumeMounts   []v1alpha1.VolumeMountForInitialization
}

func (b *BuildParams) generateImageBuildJobYAML() (string, error) {
	tmpl, err := template.New("image-build-job").Parse(jobTemplate)
	if err != nil {
		return "", err
	}

	// Add default docker context for docker build.
	b.VolumeMounts = append(b.VolumeMounts, v1alpha1.VolumeMountForInitialization{
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
	container.Args = append(container.Args, "--cache=true", "--cache-copy-layers=true", "--cache-run-layers=true")
	return job, nil
}
