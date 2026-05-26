HELM ?= helm

# values to override
IMAGE_PREFIX ?= ghcr.io/openeverest
PROVIDER_IMAGE_NAME ?= provider-percona-server-mongodb
VERSION ?= 0.1.0

.PHONY: prepare-chart
prepare-chart:
	yq eval -i '.version = "${VERSION}"' Chart.yaml
	yq eval -i '.appVersion = "${VERSION}"' Chart.yaml
	yq eval -i '.server.image = "$(IMAGE_PREFIX)/$(PROVIDER_IMAGE_NAME)"' values.yaml
	yq eval -i '.telemetry = $(TELEMETRY)' values.yaml
	$(MAKE) deps

.PHONY: release
release: prepare-chart

.PHONY: release-dev
release-dev: PROVIDER_IMAGE_NAME := provider-percona-server-mongodb-dev
release-dev: prepare-chart

.PHONY: deps
deps:
	$(HELM) version
	$(HELM) repo add percona https://percona.github.io/percona-helm-charts/
	$(HELM) dependency update .
