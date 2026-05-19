ENVTEST_K8S_VERSION = 1.29.0

IMAGE_REPOSITORY ?= quay.io/stackdome

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

CONTAINER_TOOL ?= docker

SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-30s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/deploy/crds

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

.PHONY: test-integration
test-integration: ## Run integration tests (requires Docker for Kind cluster). Use FOCUS="pattern" to run specific tests.
	go run github.com/onsi/ginkgo/v2/ginkgo -v --timeout 1h $(if $(FOCUS),--focus "$(FOCUS)") ./test/integration/... 2>&1 | tee test/integration/last-run.log

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter.
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes.
	$(GOLANGCI_LINT) run --fix

##@ Build

.PHONY: build
build: ## Build operator and reconciler binaries via mage.
	go run ./cmd/mage build:all

.PHONY: docker-build
docker-build: ## Build container images via mage.
	go run ./cmd/mage build:buildImages

.PHONY: docker-push
docker-push: ## Push container images via mage.
	go run ./cmd/mage build:pushImages

RECONCILER_IMAGE ?= $(IMAGE_REPOSITORY)/registry-config-reconciler
RECONCILER_TAG ?= v0.0.5

.PHONY: docker-build-config-reconciler
docker-build-config-reconciler: ## Build containerd-config-reconciler image.
	GOOS=linux GOARCH=amd64 go build -o .cache/image/containerd-config-reconciler/containerd-config-reconciler ./cmd/containerd-config-reconciler/
	$(CONTAINER_TOOL) build -t $(RECONCILER_IMAGE):$(RECONCILER_TAG) .cache/image/containerd-config-reconciler/

.PHONY: docker-push-config-reconciler
docker-push-config-reconciler: ## Push containerd-config-reconciler image.
	$(CONTAINER_TOOL) push $(RECONCILER_IMAGE):$(RECONCILER_TAG)

.PHONY: docker-build-sync-tools
docker-build-sync-tools: ## Build sync-tools image.
	$(CONTAINER_TOOL) build -t $(IMAGE_REPOSITORY)/tools:$(TAG) -f tools/sync-tools/Dockerfile .

.PHONY: docker-build-ssh-server
docker-build-ssh-server: ## Build ssh-server image.
	$(CONTAINER_TOOL) build -t $(IMAGE_REPOSITORY)/ssh-server:$(TAG) -f tools/ssh-server/Dockerfile .

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_TOOLS_VERSION)
ENVTEST ?= $(LOCALBIN)/setup-envtest-$(ENVTEST_VERSION)
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)

## Tool Versions
CONTROLLER_TOOLS_VERSION ?= v0.17.3
ENVTEST_VERSION ?= release-0.17
GOLANGCI_LINT_VERSION ?= v1.54.2

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint,${GOLANGCI_LINT_VERSION})

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary (ideally with version)
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f $(1) ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv "$$(echo "$(1)" | sed "s/-$(3)$$//")" $(1) ;\
}
endef
