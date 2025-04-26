//go:build mage
// +build mage

package main

import (
	"github.com/magefile/mage/mg"
)

// Dependency Versions
const (
	kindVersion              = "0.27.0"
	yqVersion                = "4.35.1"
	goimportsVersion         = "0.12.0"
	cloudProviderKindVersion = "0.6.0"
)

type Dependency mg.Namespace

func (d Dependency) All() {
	mg.Deps(
		Dependency.Kind,
		Dependency.YQ,
		Dependency.Goimports,
		Dependency.CloudProviderKind,
	)
}

// Ensure Kind dependency - Kubernetes in Docker (or Podman)
func (d Dependency) Kind() error {
	return depsDir.GoInstall("kind",
		"sigs.k8s.io/kind", kindVersion)
}

// Ensure yq - jq but for Yaml, written in Go.
func (d Dependency) YQ() error {
	return depsDir.GoInstall("yq",
		"github.com/mikefarah/yq/v4", yqVersion)
}

func (d Dependency) Goimports() error {
	return depsDir.GoInstall("go-imports",
		"golang.org/x/tools/cmd/goimports", goimportsVersion)
}

func (d Dependency) CloudProviderKind() error {
	return depsDir.GoInstall("cloud-provider-kind",
		"sigs.k8s.io/cloud-provider-kind", cloudProviderKindVersion)
}
