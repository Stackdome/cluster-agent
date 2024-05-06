package imagebuilder

import (
	"bytes"
	"fmt"
	"text/template"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
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
        - "--context=dir:///workspace"
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
      {{- range .VolumeMounts }}
      - name: {{ .PvcName }}
        persistentVolumeClaim:
          claimName: {{ .PvcName }}
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
		SubPath:            b.Context,
	})
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
	buff := bytes.NewBufferString(jobYaml)
	if err := yaml.NewYAMLOrJSONDecoder(buff, 2048).Decode(job); err != nil {
		return nil, fmt.Errorf("failed to decode Job YAML: %v", err)
	}
	return job, nil
}
