package gitsync

import (
	"bytes"
	"fmt"
	"text/template"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
	"stackdome.io/cluster-agent/api/storage/v1alpha1"
	"stackdome.io/cluster-agent/pkg/config"
)

const gitSyncJobTemplate = `
apiVersion: batch/v1
kind: Job
metadata:
  name: {{ .JobName }}
  namespace: {{ .Namespace }}
spec:
  template:
    spec:
      containers:
      - name: git-sync
        image: gitsync-image
        args:
        - "--repo={{ .RepoUrl }}"
        - "--root=/data"
        - "--link={{ .DestDir }}"
        - "--one-time"
        - "--ref={{ .Commit }}"
        volumeMounts:
        - name: target-volume
          mountPath: /data
        {{- if .HasAuth }}
        env:
        {{- if .UsernamePasswordAuth }}
        - name: GITSYNC_USERNAME
          valueFrom:
            secretKeyRef:
              name: {{ .SecretName }}
              key: {{ .UsernameKey }}
        - name: GITSYNC_PASSWORD
          valueFrom:
            secretKeyRef:
              name: {{ .SecretName }}
              key: {{ .PasswordKey }}
        {{- end }}
        {{- if .PersonalAccessTokenAuth }}
        - name: GITSYNC_USERNAME
          value: "git"
        - name: GITSYNC_PASSWORD
          valueFrom:
            secretKeyRef:
              name: {{ .SecretName }}
              key: {{ .PasswordKey }}
        {{- end }}
        {{- end }}
      restartPolicy: OnFailure
      volumes:
      - name: target-volume
        persistentVolumeClaim:
          claimName: {{ .PvcName }}
`

// GitSyncParams defines the parameters for generating a git-sync job
type GitSyncParams struct {
	JobName                 string
	Namespace               string
	PvcName                 string
	RepoUrl                 string
	Commit                  string
	HasAuth                 bool
	UsernamePasswordAuth    bool
	PersonalAccessTokenAuth bool
	SecretName              string
	UsernameKey             string
	PasswordKey             string
	DestDir                 string
}

// generateGitSyncJobYAML generates the YAML for a git-sync job
func (g *GitSyncParams) generateGitSyncJobYAML() (string, error) {
	tmpl, err := template.New("git-sync-job").Parse(gitSyncJobTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, g)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}

// GenerateGitSyncJob creates a Kubernetes Job for syncing a Git repository
func GenerateGitSyncJob(jobName string, params GitSyncParams) (*batchv1.Job, error) {
	params.JobName = jobName
	jobYaml, err := params.generateGitSyncJobYAML()
	if err != nil {
		return nil, err
	}

	job := &batchv1.Job{}
	if err := yaml.Unmarshal([]byte(jobYaml), job); err != nil {
		return nil, fmt.Errorf("failed to decode Job YAML: %v", err)
	}

	// Set resource requirements
	container := &job.Spec.Template.Spec.Containers[0]
	container.Resources = corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
	}
	container.Image = config.GitSyncImage

	// Set TTL for automatic cleanup
	job.Spec.TTLSecondsAfterFinished = ptr.To(int32(60 * 30)) // 30 minutes

	return job, nil
}

// BuildGitSyncParams creates parameters for a git-sync job from a Volume object
func BuildGitSyncParams(volume *v1alpha1.Volume, destinationDir string) (GitSyncParams, error) {
	if volume.Spec.Source == nil || volume.Spec.Source.GitRepo == nil {
		return GitSyncParams{}, fmt.Errorf("volume does not have a git repo source")
	}

	gitSource := volume.Spec.Source.GitRepo
	params := GitSyncParams{
		Namespace: volume.Namespace,
		PvcName:   volume.Status.PvcName,
		RepoUrl:   gitSource.RepoUrl,
		DestDir:   destinationDir,
	}

	if gitSource.Revision.Commit == "" {
		return params, fmt.Errorf("git repository source must specify a commit")
	}
	params.Commit = gitSource.Revision.Commit

	// Configure authentication if provided
	if gitSource.Auth != nil {
		params.HasAuth = true

		if gitSource.Auth.UsernamePasswordAuthRef != nil {
			params.UsernamePasswordAuth = true
			params.SecretName = gitSource.Auth.UsernamePasswordAuthRef.SecretRef.Name
			params.UsernameKey = gitSource.Auth.UsernamePasswordAuthRef.UsernameKey
			params.PasswordKey = gitSource.Auth.UsernamePasswordAuthRef.PasswordKey
		} else if gitSource.Auth.PersonalAccessTokenRef != nil {
			params.PersonalAccessTokenAuth = true
			params.SecretName = gitSource.Auth.PersonalAccessTokenRef.SecretRef.Name
			params.UsernameKey = gitSource.Auth.PersonalAccessTokenRef.UsernameKey
			params.PasswordKey = gitSource.Auth.PersonalAccessTokenRef.PasswordKey
		}
	}

	return params, nil
}
