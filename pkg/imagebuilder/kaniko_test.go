package imagebuilder

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
)

var _ = Describe("GenerateImageBuildJob", func() {
	Context("with build args", func() {
		It("includes --build-arg flags for each argument", func() {
			params := NewBuildParamsBuilder().
				WithJobName("test-build").
				WithNamespace("default").
				WithDestination("registry.example.com/myapp:v1").
				WithInsecureRegistry(false).
				WithDockerfilePath("Dockerfile").
				WithContextPath("/").
				WithSource(&Source{
					Volume: &VolumeSource{PvcName: "test-pvc"},
				}).
				WithBuildArgs([]ResolvedBuildArg{
					{Name: "BRANCH_NAME", Value: "main"},
					{Name: "CUSTOM_TOKEN", Value: "secret-value"},
				}).
				Build()

			job, err := GenerateImageBuildJob(params)
			Expect(err).NotTo(HaveOccurred())

			args := job.Spec.Template.Spec.Containers[0].Args
			Expect(args).To(ContainElement("--build-arg=BRANCH_NAME=main"))
			Expect(args).To(ContainElement("--build-arg=CUSTOM_TOKEN=secret-value"))
		})
	})

	Context("without build args", func() {
		It("does not include any --build-arg flags", func() {
			params := NewBuildParamsBuilder().
				WithJobName("test-build").
				WithNamespace("default").
				WithDestination("registry.example.com/myapp:v1").
				WithInsecureRegistry(false).
				WithDockerfilePath("Dockerfile").
				WithContextPath("/").
				WithSource(&Source{
					Volume: &VolumeSource{PvcName: "test-pvc"},
				}).
				Build()

			job, err := GenerateImageBuildJob(params)
			Expect(err).NotTo(HaveOccurred())

			for _, arg := range job.Spec.Template.Spec.Containers[0].Args {
				Expect(arg).NotTo(HavePrefix("--build-arg="))
			}
		})
	})

	Context("with a single destination", func() {
		It("uses the destination verbatim without duplicating the image name", func() {
			params := NewBuildParamsBuilder().
				WithJobName("api-server-abc123-build").
				WithNamespace("ns").
				WithDestination("registry.local/team/app:abc123").
				WithInsecureRegistry(false).
				WithDockerfilePath("Dockerfile").
				WithContextPath("/").
				WithSource(&Source{GitRepo: &GitRepoBuildSource{
					Repo:     &corev1alpha1.GitRepoSource{RepoUrl: "https://github.com/org/repo"},
					Revision: &corev1alpha1.GitRepoRevision{},
				}}).
				Build()

			job, err := GenerateImageBuildJob(params)
			Expect(err).NotTo(HaveOccurred())

			args := job.Spec.Template.Spec.Containers[0].Args
			joined := strings.Join(args, " ")

			Expect(joined).To(ContainSubstring("--destination=registry.local/team/app:abc123"))
			Expect(joined).NotTo(ContainSubstring("api-server/api-server"))
		})

		It("omits insecure flags when insecure is false", func() {
			params := NewBuildParamsBuilder().
				WithJobName("api-server-abc123-build").
				WithNamespace("ns").
				WithDestination("registry.local/team/app:abc123").
				WithInsecureRegistry(false).
				WithDockerfilePath("Dockerfile").
				WithContextPath("/").
				WithSource(&Source{GitRepo: &GitRepoBuildSource{
					Repo:     &corev1alpha1.GitRepoSource{RepoUrl: "https://github.com/org/repo"},
					Revision: &corev1alpha1.GitRepoRevision{},
				}}).
				Build()

			job, err := GenerateImageBuildJob(params)
			Expect(err).NotTo(HaveOccurred())

			joined := strings.Join(job.Spec.Template.Spec.Containers[0].Args, " ")
			Expect(joined).NotTo(ContainSubstring("--insecure"))
		})

		It("returns the destination from ImageUrl()", func() {
			params := NewBuildParamsBuilder().
				WithDestination("registry.local/team/app:abc123").
				Build()

			Expect(params.ImageUrl()).To(Equal("registry.local/team/app:abc123"))
		})
	})

	Context("with insecure registry", func() {
		It("includes insecure flags", func() {
			params := NewBuildParamsBuilder().
				WithJobName("build-insecure").
				WithNamespace("ns").
				WithDestination("registry.local/team/app:abc123").
				WithInsecureRegistry(true).
				WithDockerfilePath("Dockerfile").
				WithContextPath("/").
				WithSource(&Source{Volume: &VolumeSource{PvcName: "pvc"}}).
				Build()

			job, err := GenerateImageBuildJob(params)
			Expect(err).NotTo(HaveOccurred())

			joined := strings.Join(job.Spec.Template.Spec.Containers[0].Args, " ")
			Expect(joined).To(ContainSubstring("--insecure=true"))
		})
	})
})

var _ = Describe("buildGitContextUrl", func() {
	DescribeTable("constructs the correct Kaniko git context URL",
		func(repoUrl string, revision corev1alpha1.GitRepoRevision, expected string) {
			repo := &corev1alpha1.GitRepoSource{RepoUrl: repoUrl}
			Expect(buildGitContextUrl(repo, &revision)).To(Equal(expected))
		},
		Entry("branch + commit",
			"https://github.com/org/repo",
			corev1alpha1.GitRepoRevision{
				Branch: "main",
				Commit: "abc123",
			},
			"git://github.com/org/repo#refs/heads/main#abc123",
		),
		Entry("tag + commit",
			"https://github.com/org/repo",
			corev1alpha1.GitRepoRevision{
				Tag:    "v1.0.0",
				Commit: "abc123",
			},
			"git://github.com/org/repo#refs/tags/v1.0.0#abc123",
		),
		Entry("branch takes precedence over tag",
			"https://github.com/org/repo",
			corev1alpha1.GitRepoRevision{
				Branch: "main",
				Tag:    "v1.0.0",
				Commit: "abc123",
			},
			"git://github.com/org/repo#refs/heads/main#abc123",
		),
		Entry("commit only (no ref)",
			"https://github.com/org/repo",
			corev1alpha1.GitRepoRevision{
				Commit: "abc123",
			},
			"git://github.com/org/repo#abc123",
		),
		Entry("branch only (no commit)",
			"https://github.com/org/repo",
			corev1alpha1.GitRepoRevision{
				Branch: "main",
			},
			"git://github.com/org/repo#refs/heads/main",
		),
		Entry("git:// URL passthrough",
			"git://github.com/org/repo",
			corev1alpha1.GitRepoRevision{
				Branch: "main",
				Commit: "abc123",
			},
			"git://github.com/org/repo#refs/heads/main#abc123",
		),
		Entry("bare host URL gets git:// prefix",
			"github.com/org/repo",
			corev1alpha1.GitRepoRevision{
				Branch: "main",
				Commit: "abc123",
			},
			"git://github.com/org/repo#refs/heads/main#abc123",
		),
	)
})
