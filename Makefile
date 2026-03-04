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

# controller-gen version
CONTROLLER_TOOLS_VERSION ?= v0.18.0
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_TOOLS_VERSION)

# yq version for YAML processing
YQ_VERSION ?= v4.44.6
YQ ?= $(LOCALBIN)/yq-$(YQ_VERSION)

# Helm chart directory
CHART_DIR ?= charts/provider-percona-server-mongodb

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: run
run: generate ## Run the provider locally.
	go run cmd/provider/main.go

##@ Code Generation

.PHONY: manifests
manifests: controller-gen ## Generate RBAC manifests using controller-gen from kubebuilder markers.
	$(CONTROLLER_GEN) rbac:roleName=manager-role paths="./..." output:rbac:dir=config/rbac

.PHONY: helm-sync-rbac
helm-sync-rbac: yq ## Sync generated RBAC rules into the Helm chart.
	@echo "Syncing RBAC rules from config/rbac/role.yaml to Helm chart..."
	@$(YQ) '.rules' config/rbac/role.yaml > $(CHART_DIR)/generated/rbac-rules.yaml
	@echo "Done."

.PHONY: generate
generate: manifests helm-sync-rbac ## Run all code generation (RBAC + Helm sync + provider spec from definition/).
	go generate ./...
	@echo "All generation complete."

.PHONY: verify
verify: ## Verify that generated files are up-to-date (for CI).
	@$(MAKE) generate
	@if git diff --quiet -- config/ $(CHART_DIR)/generated/; then \
		echo "Generated files are up-to-date."; \
	else \
		echo "ERROR: Generated files are out of date. Run 'make generate' and commit the changes."; \
		git diff -- config/ $(CHART_DIR)/generated/; \
		exit 1; \
	fi

##@ Testing

.PHONY: test-integration
test-integration: ## Run integration tests against K8S cluster.
	. ./test/vars.sh && kubectl kuttl test --config ./test/integration/kuttl.yaml

##@ Deployment (legacy — prefer Helm targets)

.PHONY: install
install: ## Install CRDs, upstream operator, and Provider CR (dev shortcut).
	kubectl apply -f https://raw.githubusercontent.com/openeverest/openeverest/v2/config/crd/bases/core.openeverest.io_providers.yaml
	kubectl apply -f https://raw.githubusercontent.com/openeverest/openeverest/v2/config/crd/bases/core.openeverest.io_instances.yaml
	kubectl apply --server-side -f https://raw.githubusercontent.com/percona/percona-server-mongodb-operator/v1.21.1/deploy/bundle.yaml
	kubectl apply -f provider.yaml

.PHONY: uninstall
uninstall: ## Uninstall CRDs, upstream operator, and Provider CR.
	kubectl delete -f provider.yaml
	kubectl delete -f https://raw.githubusercontent.com/openeverest/openeverest/v2/config/crd/bases/core.openeverest.io_providers.yaml
	kubectl delete -f https://raw.githubusercontent.com/openeverest/openeverest/v2/config/crd/bases/core.openeverest.io_instances.yaml
	kubectl delete -f https://raw.githubusercontent.com/percona/percona-server-mongodb-operator/v1.21.1/deploy/bundle.yaml

##@ Helm

.PHONY: helm-install
helm-install: ## Install the provider using Helm.
	helm install provider-percona-server-mongodb $(CHART_DIR) --create-namespace

.PHONY: helm-upgrade
helm-upgrade: ## Upgrade the provider using Helm.
	helm upgrade provider-percona-server-mongodb $(CHART_DIR)

.PHONY: helm-uninstall
helm-uninstall: ## Uninstall the provider using Helm.
	helm uninstall provider-percona-server-mongodb

.PHONY: helm-template
helm-template: ## Render Helm chart templates locally (dry-run).
	helm template provider-percona-server-mongodb $(CHART_DIR)

##@ Local Development Cluster

.PHONY: k3d-cluster-up
k3d-cluster-up: ## Create a K8S cluster for testing.
	$(info Creating K3D cluster for testing)
	k3d cluster create --config ./dev/k3d_config.yaml

.PHONY: k3d-cluster-down
k3d-cluster-down: ## Delete the K8S test cluster.
	$(info Destroying K3D test cluster)
	k3d cluster delete --config ./dev/k3d_config.yaml

.PHONY: k3d-cluster-reset
k3d-cluster-reset: k3d-cluster-down k3d-cluster-up ## Reset the K8S cluster for testing.

##@ Build

.PHONY: build
build: generate ## Build provider binary.
	go build -o bin/provider cmd/provider/main.go

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image.
	$(CONTAINER_TOOL) push ${IMG}

##@ Tool Dependencies

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Install controller-gen.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: yq
yq: $(YQ) ## Install yq.
$(YQ): $(LOCALBIN)
	@echo "Installing yq $(YQ_VERSION)..."
	@GOBIN=$(LOCALBIN) go install github.com/mikefarah/yq/v4@$(YQ_VERSION) && mv $(LOCALBIN)/yq $(YQ)

# go-install-tool will 'go install' any package with custom target and target name.
define go-install-tool
@[ -f $(1) ] || { \
set -e; \
package=$(2)@$(3); \
echo "Installing $${package}"; \
GOBIN=$(LOCALBIN) go install $${package}; \
mv -f $$(echo "$(1)" | sed "s/-$(3)$$//") $(1); \
}
endef
