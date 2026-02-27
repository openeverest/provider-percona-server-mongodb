## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Image URL to use all building/pushing image targets
IMG ?= ghcr.io/openeverest/provider-percona-server-mongodb-dev:latest

# Set kuttl test name directory optionally for running specific integration test case.
# If not set, all test cases are run.
TEST ?=

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: run
run: gen ## Run the provider locally
	go run cmd/provider/main.go

.PHONY: gen
gen: ## Generate code (including provider.yaml from provider-config.yaml + Go types).
	go generate ./...

.PHONY: generate-manifest
generate-manifest:
	go run ./cmd/generate-manifest --output provider.yaml

.PHONY: generate-openapi
generate-openapi: openapi-gen ## Generate OpenAPI definitions for custom spec types
	$(OPENAPI_GEN) \
		--output-dir ./api \
		--output-pkg github.com/openeverest/provider-percona-server-mongodb/api \
		--output-file zz_generated.openapi.go \
		--report-filename /dev/null \
		--go-header-file hack/boilerplate.go.txt \
		github.com/openeverest/provider-percona-server-mongodb/types

##@ Test

.PHONY: test-integration
test-integration: docker-build k3d-upload-image ## Run integration tests against K8S cluster. Single test usage: make test-integration TEST=<test>
	. ./test/vars.sh && kubectl kuttl test --config ./test/integration/kuttl.yaml $(if $(TEST),--test $(TEST))

.PHONY: k3d-upload-image
k3d-upload-image: # Upload an image to K3D testing cluster.
	$(info Uploading image=$(IMG) to K3D testing cluster)
	k3d image import -c provider-psmdb-test -m direct $(IMG)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: install
install: manifests kustomize ## TODO: handle CRDs locally, gitsubmodules?
	kubectl apply -f https://raw.githubusercontent.com/openeverest/openeverest/v2/config/crd/bases/core.openeverest.io_providers.yaml
	kubectl apply -f https://raw.githubusercontent.com/openeverest/openeverest/v2/config/crd/bases/core.openeverest.io_instances.yaml
	kubectl apply --server-side -f https://raw.githubusercontent.com/percona/percona-server-mongodb-operator/v1.21.1/deploy/bundle.yaml
	kubectl apply -f provider.yaml

.PHONY: uninstall
uninstall: manifests kustomize ## TODO: handle CRDs locally, gitsubmodules?
	kubectl delete -f provider.yaml
	kubectl delete -f https://raw.githubusercontent.com/openeverest/openeverest/v2/config/crd/bases/core.openeverest.io_providers.yaml
	kubectl delete -f https://raw.githubusercontent.com/openeverest/openeverest/v2/config/crd/bases/core.openeverest.io_instances.yaml
	kubectl delete -f https://raw.githubusercontent.com/percona/percona-server-mongodb-operator/v1.21.1/deploy/bundle.yaml

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: k3d-cluster-up
k3d-cluster-up: ## Create a K8S cluster for testing.
	$(info Creating K3D cluster for testing)
	k3d cluster create --config ./dev/k3d_config.yaml

.PHONY: k3d-cluster-down
k3d-cluster-down: ## Create a K8S cluster for testing.
	$(info Destroying K3D test cluster)
	k3d cluster delete --config ./dev/k3d_config.yaml

.PHONY: k3d-cluster-reset
k3d-cluster-reset: k3d-cluster-down k3d-cluster-up ## Reset the K8S cluster for testing.

##@ Build

.PHONY: build
build: gen ## Build manager binary.
	go build -o bin/provider cmd/provider/main.go

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

##@ Dependencies

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.20.1

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
ifeq (,$(wildcard $(KUSTOMIZE)))
	GOBIN=$(LOCALBIN) GOOS=$(OS) GOARCH=$(ARCH) go install sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VERSION)
	mv $(LOCALBIN)/kustomize $(KUSTOMIZE)
endif

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
ifeq (,$(wildcard $(CONTROLLER_GEN)))
	GOBIN=$(LOCALBIN) GOOS=$(OS) GOARCH=$(ARCH) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)
	mv $(LOCALBIN)/controller-gen $(CONTROLLER_GEN)
endif
