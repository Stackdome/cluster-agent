package imagebuilder

import (
	"bytes"
	"fmt"
	"text/template"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
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
        - name: workspace
          mountPath: /workspace
          subPath: {{ .Context }}
      restartPolicy: Never
      volumes:
      - name: workspace
        persistentVolumeClaim:
          claimName: {{ .PVCName }}
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
}

func (b *BuildParams) generateImageBuildJobYAML() (string, error) {
	tmpl, err := template.New("image-build-job").Parse(jobTemplate)
	if err != nil {
		return "", err
	}

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
	if err := yaml.NewYAMLOrJSONDecoder(buff, 1024).Decode(job); err != nil {
		return nil, fmt.Errorf("failed to decode Job YAML: %v", err)
	}
	return job, nil
}
