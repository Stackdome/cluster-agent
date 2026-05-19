package fixtures

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
)

const (
	registryCredNamespace = "stackdome-registry"
)

func ClusterRegistryCredentialsSecret(name, namespace, username, password string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		StringData: map[string]string{
			"username": username,
			"password": password,
		},
	}
}

func SimpleClusterRegistry(name, credSecretName string, port int32) *registryv1alpha1.ClusterRegistry {
	return &registryv1alpha1.ClusterRegistry{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: registryv1alpha1.ClusterRegistrySpec{
			Owner: registryv1alpha1.RegistryOwner{
				Type: "Organization",
				ID:   "e2e-test",
			},
			Storage: registryv1alpha1.RegistryStorageSpec{
				Size: "2Gi",
			},
			Auth: &registryv1alpha1.RegistryAuthSpec{
				HtPasswordCredentials: &registryv1alpha1.HtPasswordCredentialsSpec{
					CredentialsRef: &corev1alpha1.CredentialSecretKeyPair{
						SecretRef: corev1.SecretReference{
							Name:      credSecretName,
							Namespace: registryCredNamespace,
						},
						UsernameKey: "username",
						PasswordKey: "password",
					},
				},
			},
			Port: port,
		},
	}
}

func ClusterRegistryWithRetention(name, credSecretName string, port int32) *registryv1alpha1.ClusterRegistry {
	reg := SimpleClusterRegistry(name, credSecretName, port)
	tagsPerRepo := int32(5)
	reg.Spec.RetentionPolicy = &registryv1alpha1.RetentionPolicySpec{
		TagsPerRepo:    &tagsPerRepo,
		DeleteUntagged: true,
	}
	return reg
}
