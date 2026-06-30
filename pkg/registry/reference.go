package registry

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	corev1alpha1 "stackdome.io/cluster-agent/api/core/v1alpha1"
	registryv1alpha1 "stackdome.io/cluster-agent/api/registry/v1alpha1"
)

type ResolvedRepository struct {
	Host       string
	Repository string
	Tag        string
	Insecure   bool
	AuthURL    string
}

func (r ResolvedRepository) Reference() string {
	return fmt.Sprintf("%s/%s:%s", r.Host, r.Repository, r.Tag)
}

func ResolveTag(policy *corev1alpha1.ImageTagPolicy, sourceRevision string) string {
	if policy != nil && policy.Fixed != nil && policy.Fixed.Tag != "" {
		return policy.Fixed.Tag
	}
	sanitize := true
	if policy != nil && policy.SourceRevision != nil {
		sanitize = policy.SourceRevision.Sanitize
	}
	if sanitize {
		return SanitizeTag(sourceRevision)
	}
	return sourceRevision
}

var tagInvalid = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func SanitizeTag(s string) string {
	s = strings.ToLower(s)
	s = tagInvalid.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-._")
	if len(s) > 128 {
		s = s[:128]
	}
	return s
}

func ResolveImageRepository(ctx context.Context, c client.Client, namespace string,
	spec corev1alpha1.ImageRepositorySpec, sourceRevision string) (ResolvedRepository, error) {

	out := ResolvedRepository{
		Repository: spec.Repository,
		Tag:        ResolveTag(spec.TagPolicy, sourceRevision),
	}

	switch {
	case spec.ClusterRegistryRef != nil:
		reg := &registryv1alpha1.ClusterRegistry{}
		key := client.ObjectKey{Name: spec.ClusterRegistryRef.Name, Namespace: namespace}
		if err := c.Get(ctx, key, reg); err != nil {
			if apierrors.IsNotFound(err) {
				return out, fmt.Errorf("cluster registry %q not found", spec.ClusterRegistryRef.Name)
			}
			return out, fmt.Errorf("failed to get cluster registry %q: %w", spec.ClusterRegistryRef.Name, err)
		}
		host, err := hostFromURL(reg.Status.InternalURL)
		if err != nil {
			return out, fmt.Errorf("invalid cluster registry internalUrl %q: %w", reg.Status.InternalURL, err)
		}
		out.Host = host
		out.Insecure = true
		out.AuthURL = host

	case spec.External != nil:
		out.Host = spec.External.Host
		if spec.External.TLS != nil {
			out.Insecure = spec.External.TLS.Insecure
		}
		out.AuthURL = authURLForHost(spec.External.Host)
	default:
		return out, fmt.Errorf("image repository spec has neither clusterRegistryRef nor external")
	}

	return out, nil
}

func hostFromURL(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return u.Host, nil
}

func authURLForHost(host string) string {
	if host == "docker.io" || host == "index.docker.io" {
		return "https://index.docker.io/v1/"
	}
	return host
}
