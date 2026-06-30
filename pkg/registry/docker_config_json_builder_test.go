package registry

import (
	"encoding/base64"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DockerConfigJSON", func() {
	Describe("NewDockerConfigJSON", func() {
		It("creates auth entry for a single credential", func() {
			creds := []AuthCreds{
				{Username: "user1", Password: "pass1", AuthUrl: "https://registry.example.com"},
			}
			result := NewDockerConfigJSON(creds)
			Expect(result.Auths).To(HaveLen(1))

			auth, ok := result.Auths["https://registry.example.com"]
			Expect(ok).To(BeTrue())
			Expect(auth.Auth).To(Equal(base64.StdEncoding.EncodeToString([]byte("user1:pass1"))))
		})

		It("creates auth entries for multiple credentials", func() {
			creds := []AuthCreds{
				{Username: "user1", Password: "pass1", AuthUrl: "https://registry1.example.com"},
				{Username: "user2", Password: "pass2", AuthUrl: "https://registry2.example.com"},
				{Username: "user3", Password: "pass3", AuthUrl: "https://registry3.example.com"},
			}
			result := NewDockerConfigJSON(creds)
			Expect(result.Auths).To(HaveLen(3))

			for _, cred := range creds {
				auth, ok := result.Auths[cred.AuthUrl]
				Expect(ok).To(BeTrue(), "missing auth entry for %s", cred.AuthUrl)
				expected := base64.StdEncoding.EncodeToString([]byte(cred.Username + ":" + cred.Password))
				Expect(auth.Auth).To(Equal(expected))
			}
		})

		It("returns an empty but non-nil auths map for empty credentials", func() {
			result := NewDockerConfigJSON([]AuthCreds{})
			Expect(result.Auths).To(BeEmpty())
			Expect(result.Auths).NotTo(BeNil())
		})
	})

	Describe("AsJSON", func() {
		It("produces valid JSON that round-trips", func() {
			creds := []AuthCreds{
				{Username: "myuser", Password: "mypass", AuthUrl: "https://registry.example.com"},
			}
			result := NewDockerConfigJSON(creds)

			data, err := result.AsJSON()
			Expect(err).NotTo(HaveOccurred())

			var parsed DockerConfigJSON
			Expect(json.Unmarshal(data, &parsed)).To(Succeed())
			Expect(parsed.Auths).To(HaveLen(1))

			auth, ok := parsed.Auths["https://registry.example.com"]
			Expect(ok).To(BeTrue())
			Expect(auth.Auth).To(Equal(base64.StdEncoding.EncodeToString([]byte("myuser:mypass"))))
		})
	})
})
