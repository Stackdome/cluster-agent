package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
)

const (
	StackdomeObjectGeneration = "managedobject.stackdome.io/generation"
)

// CredentialSecretKeyPair selects username and password keys from a Secret
type CredentialSecretKeyPair struct {
	// +required
	SecretRef corev1.SecretReference `json:"secretRef"`
	// UsernameKey is the key in the secret for the username
	// +required
	UsernameKey string `json:"usernameKey"`

	// PasswordKey is the key in the secret for the password
	// +required
	PasswordKey string `json:"passwordKey"`
}
