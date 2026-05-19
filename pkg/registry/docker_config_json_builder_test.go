package registry

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestNewDockerConfigJSON_SingleCred(t *testing.T) {
	creds := []AuthCreds{
		{Username: "user1", Password: "pass1", AuthUrl: "https://registry.example.com"},
	}

	result := NewDockerConfigJSON(creds)

	if len(result.Auths) != 1 {
		t.Fatalf("expected 1 auth entry, got %d", len(result.Auths))
	}

	auth, ok := result.Auths["https://registry.example.com"]
	if !ok {
		t.Fatal("expected auth entry for https://registry.example.com")
	}

	expected := base64.StdEncoding.EncodeToString([]byte("user1:pass1"))
	if auth.Auth != expected {
		t.Errorf("expected auth %q, got %q", expected, auth.Auth)
	}
}

func TestNewDockerConfigJSON_MultipleCreds(t *testing.T) {
	creds := []AuthCreds{
		{Username: "user1", Password: "pass1", AuthUrl: "https://registry1.example.com"},
		{Username: "user2", Password: "pass2", AuthUrl: "https://registry2.example.com"},
		{Username: "user3", Password: "pass3", AuthUrl: "https://registry3.example.com"},
	}

	result := NewDockerConfigJSON(creds)

	if len(result.Auths) != 3 {
		t.Fatalf("expected 3 auth entries, got %d", len(result.Auths))
	}

	for _, cred := range creds {
		auth, ok := result.Auths[cred.AuthUrl]
		if !ok {
			t.Errorf("missing auth entry for %s", cred.AuthUrl)
			continue
		}
		expected := base64.StdEncoding.EncodeToString([]byte(cred.Username + ":" + cred.Password))
		if auth.Auth != expected {
			t.Errorf("auth for %s: expected %q, got %q", cred.AuthUrl, expected, auth.Auth)
		}
	}
}

func TestNewDockerConfigJSON_Empty(t *testing.T) {
	result := NewDockerConfigJSON([]AuthCreds{})

	if len(result.Auths) != 0 {
		t.Fatalf("expected 0 auth entries, got %d", len(result.Auths))
	}

	if result.Auths == nil {
		t.Fatal("expected non-nil auths map")
	}
}

func TestDockerConfigJSON_AsJSON(t *testing.T) {
	creds := []AuthCreds{
		{Username: "myuser", Password: "mypass", AuthUrl: "https://registry.example.com"},
	}

	result := NewDockerConfigJSON(creds)

	data, err := result.AsJSON()
	if err != nil {
		t.Fatalf("AsJSON returned error: %v", err)
	}

	var parsed DockerConfigJSON
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal JSON output: %v", err)
	}

	if len(parsed.Auths) != 1 {
		t.Fatalf("expected 1 auth entry in parsed JSON, got %d", len(parsed.Auths))
	}

	auth, ok := parsed.Auths["https://registry.example.com"]
	if !ok {
		t.Fatal("expected auth entry for https://registry.example.com in parsed JSON")
	}

	expected := base64.StdEncoding.EncodeToString([]byte("myuser:mypass"))
	if auth.Auth != expected {
		t.Errorf("expected auth %q in parsed JSON, got %q", expected, auth.Auth)
	}
}
