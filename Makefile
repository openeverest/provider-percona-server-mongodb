## Tool Versions
OPENAPI_GEN_VERSION ?= v0.0.0-20250910181357-589584f1c912

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

OPENAPI_GEN ?= $(LOCALBIN)/openapi-gen

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
gen: generate-openapi generate-manifest ## Generate code.
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
test-integration: docker-build k3d-upload-image ## Run integration tests against K8S cluster. Specific test usage: make test-integration TEST=<test>
	. ./test/vars.sh && kubectl kuttl test --config ./test/integration/kuttl.yaml $(if $(TEST),--test $(TEST))

.PHONY: k3d-upload-image
k3d-upload-image:
	$(info Uploading image=$(IMG) to K3D testing cluster)
	k3d image import -c provider-psmdb-test -m direct $(IMG)

##@ Development

.PHONY: openapi-gen
openapi-gen: $(OPENAPI_GEN) ## Download openapi-gen locally if necessary
$(OPENAPI_GEN): $(LOCALBIN)
	test -s $(LOCALBIN)/openapi-gen || \
	GOBIN=$(LOCALBIN) go install k8s.io/kube-openapi/cmd/openapi-gen@$(OPENAPI_GEN_VERSION)

.PHONY: install
install: ## TODO: handle CRDs locally, gitsubmodules?
	kubectl apply -f https://raw.githubusercontent.com/openeverest/openeverest/v2/config/crd/bases/core.openeverest.io_providers.yaml
	kubectl apply -f https://raw.githubusercontent.com/openeverest/openeverest/v2/config/crd/bases/core.openeverest.io_instances.yaml
	kubectl apply --server-side -f https://raw.githubusercontent.com/percona/percona-server-mongodb-operator/v1.21.1/deploy/bundle.yaml
	kubectl apply -f provider.yaml

.PHONY: uninstall
uninstall:  ## TODO: handle CRDs locally, gitsubmodules?
	kubectl delete -f provider.yaml
	kubectl delete -f https://raw.githubusercontent.com/openeverest/openeverest/v2/config/crd/bases/core.openeverest.io_providers.yaml
	kubectl delete -f https://raw.githubusercontent.com/openeverest/openeverest/v2/config/crd/bases/core.openeverest.io_instances.yaml
	kubectl delete -f https://raw.githubusercontent.com/percona/percona-server-mongodb-operator/v1.21.1/deploy/bundle.yaml

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