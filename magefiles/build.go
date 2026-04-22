//go:build mage
// +build mage

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
	"github.com/mt-sre/client"
	"github.com/mt-sre/devkube/dev"
	imageparser "github.com/novln/docker-parser"
)

type Build mg.Namespace

// Build Tags
var (
	branch        string
	shortCommitID string
	version       string
	buildDate     string
	ldFlags       string
	imageOrg      string
)

// init build variables
func (Build) init() error {
	// Build flags
	branchCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	branchBytes, err := branchCmd.Output()
	if err != nil {
		panic(fmt.Errorf("getting git branch: %w", err))
	}
	branch = strings.TrimSpace(string(branchBytes))

	shortCommitIDCmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	shortCommitIDBytes, err := shortCommitIDCmd.Output()
	if err != nil {
		panic(fmt.Errorf("getting git short commit id"))
	}
	shortCommitID = strings.TrimSpace(string(shortCommitIDBytes))

	versionFromEnv := strings.TrimSpace(os.Getenv("VERSION"))
	if len(versionFromEnv) > 0 {
		version = versionFromEnv
	}
	if len(version) == 0 {
		version = fmt.Sprint(time.Now().UTC().Unix())
	}

	buildDate = fmt.Sprint(time.Now().UTC().Unix())
	ldFlags = fmt.Sprintf(`-X %s/internal/version.Version=%s`+
		`-X %s/internal/version.Branch=%s`+
		`-X %s/internal/version.Commit=%s`+
		`-X %s/internal/version.BuildDate=%s`,
		module, version,
		module, branch,
		module, shortCommitID,
		module, buildDate,
	)

	imageOrg = os.Getenv("IMAGE_ORG")
	if len(imageOrg) == 0 {
		imageOrg = defaultImageOrg
	}

	return nil
}

// Builds the docgen internal tool
func (Build) Docgen() {
	mg.Deps(mg.F(Build.cmd, "docgen", "", ""))
}

// Builds binaries from /cmd directory.
func (Build) cmd(cmd, goos, goarch string) error {
	mg.Deps(Build.init)

	env := map[string]string{
		"GOFLAGS": "",
		"LDFLAGS": ldFlags,
	}

	_, cgoOK := os.LookupEnv("CGO_ENABLED")
	if !cgoOK {
		env["CGO_ENABLED"] = "0"
	}

	bin := path.Join("bin", cmd)

	if len(goos) != 0 && len(goarch) != 0 {
		// change bin path to point to a subdirectory when cross compiling
		bin = path.Join("bin", goos+"_"+goarch, cmd)
		env["GOOS"] = goos
		env["GOARCH"] = goarch
	}

	switch cmd {
	case "cluster-agent-manager":
		if err := sh.RunWithV(
			env,
			"go", "build", "-v", "-o", bin, "./cmd/cluster-agent/"+cmd+".go",
		); err != nil {
			return fmt.Errorf("compiling cluster-agent-manager: %v", err)
		}
	case "containerd-config-reconciler":
		if err := sh.RunWithV(
			env,
			"go", "build", "-v", "-o", bin, "./cmd/containerd-config-reconciler/"+cmd+".go",
		); err != nil {
			return fmt.Errorf("compiling cmd/%s: %w", cmd, err)
		}
	default:
		return fmt.Errorf("unexpected cmd '%s'. Dont know how to build!", cmd)
	}
	return nil
}

// Default build target for CI/CD to build binaries
func (Build) All() {
	mg.Deps(
		mg.F(Build.cmd, "cluster-agent-manager", "linux", "amd64"),
		mg.F(Build.cmd, "containerd-config-reconciler", "linux", "amd64"),
	)
}

func (Build) BuildImages() {
	mg.Deps(
		mg.F(Build.ImageBuild, "cluster-agent-manager"),
		mg.F(Build.ImageBuild, "containerd-config-reconciler"),
	)
}

func (Build) PushImages() {
	mg.Deps(
		mg.F(Build.imagePush, "cluster-agent-manager"),
		mg.F(Build.imagePush, "containerd-config-reconciler"),
	)
}

func (Build) PushImagesOnce() {
	mg.Deps(
		mg.F(Build.imagePushOnce, "cluster-agent-manager"),
		mg.F(Build.imagePushOnce, "containerd-config-reconciler"),
	)
}

func (Build) imagePush(imageName string) error {
	mg.SerialDeps(setupContainerRuntime, Build.init)

	pushInfo := newImagePushInfo(imageName)
	buildImageDep := mg.F(Build.ImageBuild, imageName)

	return dev.PushImage(pushInfo, buildImageDep)
}

func (b Build) imagePushOnce(imageName string) error {
	mg.SerialDeps(
		Build.init,
	)

	ok, err := b.imageExists(context.Background(), imageName)
	if err != nil {
		return fmt.Errorf("checking if image %q exists: %w", imageName, err)
	}

	if ok {
		fmt.Fprintf(os.Stdout, "skipping image %q since it is already up-to-date\n", imageName)

		return nil
	}
	return b.imagePush(imageName)
}

func (Build) imageExists(ctx context.Context, name string) (bool, error) {
	ref, err := imageparser.Parse(imageURL(name))
	if err != nil {
		return false, fmt.Errorf("parsing image reference: %w", err)
	}

	url := url.URL{
		Scheme: "https",
		Host:   ref.Registry(),
		Path:   path.Join("v2", ref.ShortName(), "manifests", ref.Tag()),
	}

	c := client.NewClient()
	res, err := c.Head(ctx, url.String())
	if err != nil {
		return false, fmt.Errorf("sending HTTP request: %w", err)
	}

	defer res.Body.Close()

	return res.StatusCode == http.StatusOK, nil
}

func (b Build) ImageBuild(cmd string) error {
	mg.SerialDeps(setupContainerRuntime, b.init)

	// clean/prepare cache directory
	imageCacheDir := path.Join(cacheDir, "image", cmd)
	if err := cleanImageCache(imageCacheDir); err != nil {
		return fmt.Errorf("cleaning cache: %w", err)
	}

	deps := []interface{}{
		mg.F(Build.cmd, cmd, "linux", "amd64"),
		mg.F(populateCmdCache, imageCacheDir, cmd),
	}
	imageBuildInfo := newImageBuildInfo(cmd, imageCacheDir)
	return dev.BuildImage(imageBuildInfo, deps)
}

func newImageBuildInfo(imageName, imageCacheDir string) *dev.ImageBuildInfo {
	imageTag := imageURL(imageName)
	return &dev.ImageBuildInfo{
		ImageTag:      imageTag,
		CacheDir:      imageCacheDir,
		ContainerFile: "",
		ContextDir:    imageCacheDir,
		Runtime:       containerRuntime,
	}
}

func newImagePushInfo(imageName string) *dev.ImagePushInfo {
	imageTag := imageURL(imageName)
	return &dev.ImagePushInfo{
		ImageTag:   imageTag,
		CacheDir:   cacheDir,
		Runtime:    containerRuntime,
		DigestFile: "",
	}
}

func cleanImageCache(imageCacheDir string) error {
	if err := os.RemoveAll(imageCacheDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting image cache dir: %w", err)
	}
	if err := os.Remove(imageCacheDir + ".tar"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting image tar: %w", err)
	}
	if err := os.MkdirAll(imageCacheDir, os.ModePerm); err != nil {
		return fmt.Errorf("create image cache dir: %w", err)
	}
	return nil
}

func populateCmdCache(imageCacheDir, cmd string) error {
	commands := [][]string{
		{"cp", "-a", "bin/linux_amd64/" + cmd, imageCacheDir + "/" + cmd},
		{"cp", "-a", "config/docker/" + cmd + ".Dockerfile", imageCacheDir + "/Dockerfile"},
	}
	for _, command := range commands {
		if err := sh.Run(command[0], command[1:]...); err != nil {
			return fmt.Errorf("running %q: %w", strings.Join(command, " "), err)
		}
	}
	return nil
}

func imageURL(name string) string {
	// Build.init must be run before this function to set `imageOrg` and `version` variables
	envvar := strings.ReplaceAll(strings.ToUpper(name), "-", "_") + "_IMAGE"
	if url := os.Getenv(envvar); len(url) != 0 {
		return url
	}
	if len(version) == 0 {
		panic("empty version, refusing to return container image URL")
	}
	return imageOrg + "/" + name + ":" + version
}
