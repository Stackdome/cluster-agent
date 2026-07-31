package errorpages

import (
	"embed"
	"path"
)

//go:embed assets/*
var assetsFS embed.FS

// Assets maps a filename to its page content, compiled into the binary so the
// agent can serve them without a separate deployment or any disk dependency.
var Assets = mustLoadAssets()

func mustLoadAssets() map[string]string {
	entries, err := assetsFS.ReadDir("assets")
	if err != nil {
		panic(err)
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		b, err := assetsFS.ReadFile(path.Join("assets", e.Name()))
		if err != nil {
			panic(err)
		}
		out[e.Name()] = string(b)
	}
	return out
}
