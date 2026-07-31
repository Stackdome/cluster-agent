package errorpages

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Assets", func() {
	It("embeds a page for every status Traefik can hand us", func() {
		Expect(Assets).To(HaveKey("404.html"))
		Expect(Assets).To(HaveKey("500.html"))
		Expect(Assets).To(HaveKey("502.html"))
		Expect(Assets).To(HaveKey("503.html"))
	})

	It("makes no external requests", func() {
		// These are served in clusters with no guaranteed internet egress; a
		// blocked font or script request would leave the page unstyled.
		for name, content := range Assets {
			if !strings.HasSuffix(name, ".html") {
				continue
			}
			for _, forbidden := range []string{"http://", "https://", "//fonts.", "<script"} {
				Expect(content).NotTo(ContainSubstring(forbidden),
					"%s must be fully self-contained", name)
			}
		}
	})

	It("explains the likely cause on the 502 page", func() {
		// After this work the most common cause of a 502 is an app not
		// listening on its declared port, so the copy must say so.
		Expect(Assets["502.html"]).To(ContainSubstring("port"))
		Expect(Assets["502.html"]).NotTo(ContainSubstring("Bad Gateway"))
	})
})
