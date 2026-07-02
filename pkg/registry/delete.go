package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// DeleteImage removes a tagged image from a registry. imageRef is a full
// "host/repository:tag" reference (e.g. an ImageBuild's status.imageUrl); set
// insecure for plain-HTTP registries such as the in-cluster one.
//
// It is idempotent — a missing repo/tag is treated as success — so it is safe to
// call from a finalizer that may run more than once. It is intended only for the
// internal managed registry; callers must NOT invoke it for external registries
// (Quay, Docker Hub, …), which are the customer's to manage.
func DeleteImage(ctx context.Context, imageRef string, insecure bool, username, password string) error {
	var nameOpts []name.Option
	if insecure {
		nameOpts = append(nameOpts, name.Insecure)
	}
	tag, err := name.NewTag(imageRef, nameOpts...)
	if err != nil {
		return fmt.Errorf("parse reference %q: %w", imageRef, err)
	}

	opts := []remote.Option{remote.WithContext(ctx)}
	if username != "" {
		opts = append(opts, remote.WithAuth(&authn.Basic{Username: username, Password: password}))
	}

	// Delete by TAG, not by digest. Deleting a manifest by digest would remove
	// every tag pointing at it — and different builds routinely share a digest
	// (e.g. a commit that only changes files outside the build context produces a
	// byte-identical image under a new tag). Removing only this tag leaves such
	// siblings intact; the registry reclaims the manifest via deleteUntagged GC
	// once no tag references it.
	if err := remote.Delete(tag, opts...); err != nil {
		if isNotFoundErr(err) {
			return nil // tag already gone
		}
		return fmt.Errorf("delete tag %s: %w", imageRef, err)
	}
	return nil
}

// isNotFoundErr reports whether a go-containerregistry error is an HTTP 404.
func isNotFoundErr(err error) bool {
	var terr *transport.Error
	if errors.As(err, &terr) {
		return terr.StatusCode == http.StatusNotFound
	}
	return false
}
