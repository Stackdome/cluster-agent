package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GetGitRevisionString", func() {
	It("returns the commit SHA", func() {
		rev := &GitRepoRevision{Commit: "abc123def456"}
		Expect(rev.GetGitRevisionString()).To(Equal("abc123def456"))
	})

	It("returns empty string when commit is empty", func() {
		rev := &GitRepoRevision{Branch: "main"}
		Expect(rev.GetGitRevisionString()).To(BeEmpty())
	})

	It("returns commit SHA even when branch and tag are also set", func() {
		rev := &GitRepoRevision{
			Branch: "main",
			Tag:    "v1.0.0",
			Commit: "abc123",
		}
		Expect(rev.GetGitRevisionString()).To(Equal("abc123"))
	})
})
