package fixtures

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
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
