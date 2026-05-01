package fixtures

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

const (
	BuildSourceRepoURL        = "https://github.com/ashishmax31/test-private-repo.git"
	BuildSourceBranch         = "main"
	BuildSourceResourceName   = "todo-app"
	RegistryDockerConfigSecret = "registry-docker-config"
)

// SimpleStack creates a Stack with a single nginx-based StackResource.
func SimpleStack(name string) *corev1alpha1.Stack {
	return &corev1alpha1.Stack{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: defaultNamespace,
		},
		Spec: corev1alpha1.StackSpec{
			StackResources: []corev1alpha1.StackResourceTemplate{
				{
					Name: name + "-web",
					Spec: corev1alpha1.StackResourceSpec{
						ImageSpec: &corev1alpha1.ImageSpec{
							Image: "nginx:1.25-alpine",
						},
						Ports: []corev1alpha1.Port{
							{
								Number: 80,
								FQDN:   name + "-web.local",
							},
						},
					},
				},
			},
		},
	}
}

// MultiResourceStack creates a Stack with a backend and frontend resource.
// The frontend has an env var that references the backend via interpolation.
func MultiResourceStack(name string) *corev1alpha1.Stack {
	backendName := name + "-backend"
	frontendName := name + "-frontend"
	return &corev1alpha1.Stack{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: defaultNamespace,
		},
		Spec: corev1alpha1.StackSpec{
			StackResources: []corev1alpha1.StackResourceTemplate{
				{
					Name: backendName,
					Spec: corev1alpha1.StackResourceSpec{
						ImageSpec: &corev1alpha1.ImageSpec{
							Image: "nginx:1.25-alpine",
						},
						Ports: []corev1alpha1.Port{
							{
								Number: 8080,
								FQDN:   backendName + ".local",
							},
						},
					},
				},
				{
					Name: frontendName,
					Spec: corev1alpha1.StackResourceSpec{
						ImageSpec: &corev1alpha1.ImageSpec{
							Image: "nginx:1.25-alpine",
						},
						Ports: []corev1alpha1.Port{
							{
								Number: 80,
								FQDN:   frontendName + ".local",
							},
						},
						EnvironmentVariables: []corev1alpha1.EnvironmentVariables{
							{
								Name:  "BACKEND_URL",
								Value: "{{ STACKDOME_" + envName(backendName) + "_INTERNAL }}",
							},
						},
					},
				},
			},
		},
	}
}

// StackWithDependencies creates a Stack where resource B depends on resource A.
func StackWithDependencies(name string) *corev1alpha1.Stack {
	resourceA := name + "-dep-a"
	resourceB := name + "-dep-b"
	return &corev1alpha1.Stack{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: defaultNamespace,
		},
		Spec: corev1alpha1.StackSpec{
			StackResources: []corev1alpha1.StackResourceTemplate{
				{
					Name: resourceA,
					Spec: corev1alpha1.StackResourceSpec{
						ImageSpec: &corev1alpha1.ImageSpec{
							Image: "nginx:1.25-alpine",
						},
						Ports: []corev1alpha1.Port{
							{
								Number: 80,
								FQDN:   resourceA + ".local",
							},
						},
					},
				},
				{
					Name: resourceB,
					Spec: corev1alpha1.StackResourceSpec{
						ImageSpec: &corev1alpha1.ImageSpec{
							Image: "nginx:1.25-alpine",
						},
						Ports: []corev1alpha1.Port{
							{
								Number: 80,
								FQDN:   resourceB + ".local",
							},
						},
						DependsOn: []string{resourceA},
					},
				},
			},
		},
	}
}

// StackWithEnvAndPorts creates a Stack with explicit environment variables
// and multiple ports on a single resource.
func StackWithEnvAndPorts(name string) *corev1alpha1.Stack {
	resourceName := name + "-app"
	return &corev1alpha1.Stack{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: defaultNamespace,
		},
		Spec: corev1alpha1.StackSpec{
			StackResources: []corev1alpha1.StackResourceTemplate{
				{
					Name: resourceName,
					Spec: corev1alpha1.StackResourceSpec{
						ImageSpec: &corev1alpha1.ImageSpec{
							Image: "nginx:1.25-alpine",
						},
						Ports: []corev1alpha1.Port{
							{
								Number: 8080,
								FQDN:   resourceName + "-http.local",
							},
							{
								Number: 9090,
								FQDN:   resourceName + "-metrics.local",
							},
						},
						EnvironmentVariables: []corev1alpha1.EnvironmentVariables{
							{Name: "APP_ENV", Value: "integration-test"},
							{Name: "APP_PORT", Value: "8080"},
							{Name: "LOG_LEVEL", Value: "debug"},
						},
					},
				},
			},
		},
	}
}

// StackWithInitContainer creates a Stack whose resource has an init container.
func StackWithInitContainer(name string) *corev1alpha1.Stack {
	resourceName := name + "-init"
	return &corev1alpha1.Stack{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: defaultNamespace,
		},
		Spec: corev1alpha1.StackSpec{
			StackResources: []corev1alpha1.StackResourceTemplate{
				{
					Name: resourceName,
					Spec: corev1alpha1.StackResourceSpec{
						ImageSpec: &corev1alpha1.ImageSpec{
							Image: "nginx:1.25-alpine",
						},
						Init: &corev1alpha1.InitSpec{
							Command: []string{"sh"},
							Args:    []string{"-c", "echo 'init done'"},
						},
						Ports: []corev1alpha1.Port{
							{
								Number: 80,
								FQDN:   resourceName + ".local",
							},
						},
					},
				},
			},
		},
	}
}

// StackForDeletion creates a simple Stack used to test cascade deletion.
func StackForDeletion(name string) *corev1alpha1.Stack {
	return SimpleStack(name)
}

// StackWithBuildArgs creates a Stack with a single resource that builds from a
// private git repo and uses build arguments (both inline and secret-backed).
func StackWithBuildArgs(name, registryURL, gitSecretName, buildArgSecretName string) *corev1alpha1.Stack {
	return &corev1alpha1.Stack{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: defaultNamespace,
		},
		Spec: corev1alpha1.StackSpec{
			StackResources: []corev1alpha1.StackResourceTemplate{
				{
					Name: BuildSourceResourceName,
					Spec: corev1alpha1.StackResourceSpec{
						BuildSpec: &corev1alpha1.StackResourceBuildSpec{
							SourceContext: corev1alpha1.BuildContextSource{
								Git: &corev1alpha1.GitRepoSource{
									RepoUrl: BuildSourceRepoURL,
									Auth: &corev1alpha1.GitAuth{
										PersonalAccessTokenRef: &corev1alpha1.CredentialSecretKeyPair{
											SecretRef: corev1.SecretReference{
												Name:      gitSecretName,
												Namespace: defaultNamespace,
											},
											UsernameKey: "username",
											PasswordKey: "token",
										},
									},
								},
							},
							BuildContext:   ".",
							DockerFilePath: "Dockerfile",
							SourceRevision: corev1alpha1.SourceRevisionSpec{
								GitRepo: &corev1alpha1.GitRepoRevision{
									Branch: &corev1alpha1.GitBranch{
										Name:    BuildSourceBranch,
										HeadSha: "HEAD",
									},
								},
							},
							Registry: corev1alpha1.RegistrySpec{
								RepositoryURL: registryURL,
								Insecure:      true,
								Auth: &corev1alpha1.RegistryAuth{
									Type: corev1alpha1.RegistryAuthTypeInClusterZotRegistry,
									DockerConfigAuth: &corev1alpha1.DockerConfigAuth{
										SecretKey: ".dockerconfigjson",
										SecretRef: &corev1.SecretReference{
											Name:      RegistryDockerConfigSecret,
											Namespace: defaultNamespace,
										},
									},
								},
							},
							BuildArgs: []corev1alpha1.BuildArg{
								{
									Name:  "APP_ENV",
									Value: "integration-test",
								},
								{
									Name: "BUILD_TOKEN",
									ValueFrom: &corev1alpha1.BuildArgValueSource{
										SecretKeyRef: corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: buildArgSecretName,
											},
											Key: "token",
										},
									},
								},
							},
						},
						Ports: []corev1alpha1.Port{
							{
								Number: 3000,
								FQDN:   name + ".local",
							},
						},
					},
				},
			},
		},
	}
}

// envName converts a resource name to the interpolation variable format.
// Hyphens become underscores, result is uppercased.
func envName(name string) string {
	result := make([]byte, len(name))
	for i, c := range []byte(name) {
		if c == '-' {
			result[i] = '_'
		} else if c >= 'a' && c <= 'z' {
			result[i] = c - 32
		} else {
			result[i] = c
		}
	}
	return string(result)
}
