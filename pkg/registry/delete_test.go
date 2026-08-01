package registry

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	ggcrregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestDeleteImage(t *testing.T) {
	srv := httptest.NewServer(ggcrregistry.New())
	defer srv.Close()

	repo := ResolvedRepository{Host: mustHost(t, srv.URL), Repository: "ns/app/app", Tag: "preview-1", Insecure: true}
	ref, err := name.NewTag(repo.Reference(), name.Insecure)
	if err != nil {
		t.Fatal(err)
	}

	// Push a real image at the tag.
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("seed image: %v", err)
	}
	if _, err := remote.Head(ref); err != nil {
		t.Fatalf("image should exist before delete: %v", err)
	}

	if err := DeleteImage(context.Background(), repo.Reference(), repo.Insecure, "", ""); err != nil {
		t.Fatalf("DeleteImage: %v", err)
	}

	// The tag must be gone.
	if _, err := remote.Head(ref); err == nil {
		t.Fatal("expected tag to be gone after delete")
	} else if !isNotFoundErr(err) {
		t.Fatalf("expected 404 after delete, got: %v", err)
	}
}

// TestDeleteImagePreservesSharedDigest verifies we delete only the target tag,
// not the underlying manifest — so a sibling tag that shares the same digest
// (e.g. two commits producing a byte-identical image) survives.
func TestDeleteImagePreservesSharedDigest(t *testing.T) {
	srv := httptest.NewServer(ggcrregistry.New())
	defer srv.Close()
	host := mustHost(t, srv.URL)

	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	refA, err := name.NewTag(fmt.Sprintf("%s/ns/app/app:commit-a", host), name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	refB, err := name.NewTag(fmt.Sprintf("%s/ns/app/app:commit-b", host), name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	// Both tags point at the same image (same digest).
	if err := remote.Write(refA, img); err != nil {
		t.Fatalf("seed tag A: %v", err)
	}
	if err := remote.Write(refB, img); err != nil {
		t.Fatalf("seed tag B: %v", err)
	}

	repoA := ResolvedRepository{Host: host, Repository: "ns/app/app", Tag: "commit-a", Insecure: true}
	if err := DeleteImage(context.Background(), repoA.Reference(), repoA.Insecure, "", ""); err != nil {
		t.Fatalf("DeleteImage: %v", err)
	}

	// Tag A is gone.
	if _, err := remote.Head(refA); err == nil || !isNotFoundErr(err) {
		t.Fatalf("tag A should be gone, got err=%v", err)
	}
	// Tag B, sharing the digest, must still resolve.
	if _, err := remote.Head(refB); err != nil {
		t.Fatalf("tag B should survive deletion of a shared-digest sibling: %v", err)
	}
}

func TestDeleteImageIdempotentWhenMissing(t *testing.T) {
	srv := httptest.NewServer(ggcrregistry.New())
	defer srv.Close()

	repo := ResolvedRepository{Host: mustHost(t, srv.URL), Repository: "ns/app/app", Tag: "never-pushed", Insecure: true}
	if err := DeleteImage(context.Background(), repo.Reference(), repo.Insecure, "", ""); err != nil {
		t.Fatalf("expected nil for missing tag, got %v", err)
	}
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}
