package api

// CredentialSecretKeyPair selects username and password keys from a Secret
type CredentialSecretKeyPair struct {
	// Secret is the name of the secret
	// +required
	SecretName string `json:"secretName"`

	// +required
	SecretNamespace string `json:"secretNamespace"`

	// UsernameKey is the key in the secret for the username
	// +required
	UsernameKey string `json:"usernameKey"`

	// PasswordKey is the key in the secret for the password
	// +required
	PasswordKey string `json:"passwordKey"`
}
