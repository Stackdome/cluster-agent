package registry

import (
	"encoding/base64"
	"encoding/json"
)

type DockerConfigJSON struct {
	// Username is the username for the registry
	Auths map[string]DockerConfigJSONAuth `json:"auths"`
}

type DockerConfigJSONAuth struct {
	// Auth is the base64 encoded username:password
	Auth string `json:"auth"`
}

type AuthCreds struct {
	Username string
	Password string
	AuthUrl  string
}

func NewDockerConfigJSON(creds []AuthCreds) DockerConfigJSON {
	res := DockerConfigJSON{
		Auths: make(map[string]DockerConfigJSONAuth),
	}
	for _, cred := range creds {
		res.Auths[cred.AuthUrl] = DockerConfigJSONAuth{
			Auth: base64.StdEncoding.EncodeToString([]byte(cred.Username + ":" + cred.Password)),
		}
	}
	return res
}

func (d *DockerConfigJSON) AsJSON() ([]byte, error) {
	return json.MarshalIndent(d, "", "  ")
}
