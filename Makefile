# SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and Gardener contributors
#
# SPDX-License-Identifier: Apache-2.0

EXTENSION_PREFIX            := gardener-extension
NAME                        := shoot-rsyslog-relp
NAME_ADMISSION              := $(NAME)-admission
NAME_ECHO_SERVER            := $(NAME)-echo-server
IMAGE                       := europe-docker.pkg.dev/gardener-project/public/gardener/extensions/shoot-rsyslog-relp
ECHO_SERVER_IMAGE           := europe-docker.pkg.dev/gardener-project/releases/gardener/extensions/$(NAME_ECHO_SERVER)
REPO_ROOT                   := $(shell dirname $(realpath $(lastword $(MAKEFILE_LIST))))
HACK_DIR                    := $(REPO_ROOT)/hack
REPO_TOOLS_HACK_DIR         := $(REPO_ROOT)/repo-tools/hack
VERSION                     := $(shell cat "$(REPO_ROOT)/VERSION")
EFFECTIVE_VERSION           := $(VERSION)-$(shell git rev-parse HEAD)
ECHO_SERVER_VERSION         := v0.2.0
IMAGE_TAG                   := $(EFFECTIVE_VERSION)
LD_FLAGS                    := "-w $(shell EFFECTIVE_VERSION=$(EFFECTIVE_VERSION) bash $(REPO_TOOLS_HACK_DIR)/get-build-ld-flags.sh k8s.io/component-base $(REPO_ROOT)/VERSION $(EXTENSION_PREFIX)-$(NAME))"
PARALLEL_E2E_TESTS          := 3
GARDENER_REPO_ROOT          ?= $(REPO_ROOT)/../gardener
SEED_NAME                   := provider-extensions
SEED_KUBECONFIG             := $(GARDENER_REPO_ROOT)/example/provider-extensions/seed/kubeconfig
SHOOT_NAME                  ?= local
SHOOT_NAMESPACE             ?= garden-local

ifneq ($(SEED_NAME),provider-extensions)
	SEED_KUBECONFIG := $(GARDENER_REPO_ROOT)/example/provider-extensions/seed/kubeconfig-$(SEED_NAME)
endif

ifndef ARTIFACTS
	export ARTIFACTS=/tmp/artifacts
endif

ifneq ($(strip $(shell git status --porcelain 2>/dev/null)),)
	EFFECTIVE_VERSION := $(EFFECTIVE_VERSION)-dirty
endif

#########################################
# Tools                                 #
#########################################

TOOLS_DIR := $(HACK_DIR)/tools
include $(REPO_TOOLS_HACK_DIR)/tools.mk

#################################################################
# Rules related to binary build, Docker image build and release #
#################################################################

.PHONY: install
install:
	@LD_FLAGS=$(LD_FLAGS) \
	bash $(REPO_TOOLS_HACK_DIR)/install.sh ./cmd/...

.PHONY: docker-login
docker-login:
	@gcloud auth activate-service-account --key-file .kube-secrets/gcr/gcr-readwrite.json

.PHONY: docker-images
docker-images:
	@docker build --build-arg EFFECTIVE_VERSION=$(EFFECTIVE_VERSION) -t $(IMAGE):$(IMAGE_TAG) -f Dockerfile -m 6g --target $(NAME) .
	@docker build --build-arg EFFECTIVE_VERSION=$(EFFECTIVE_VERSION) -t $(IMAGE)-admission:$(IMAGE_TAG) -f Dockerfile -m 6g --target $(NAME_ADMISSION) .

###################################################################
# Rules related to the shoot-rsysog-relp-echo-server docker image #
###################################################################

.PHONY: echo-server-docker-image
echo-server-docker-image:
	@docker build --platform linux/amd64,linux/arm64 --build-arg EFFECTIVE_VERSION=$(ECHO_SERVER_VERSION) -t $(ECHO_SERVER_IMAGE):$(ECHO_SERVER_VERSION) -t $(ECHO_SERVER_IMAGE):latest -f Dockerfile -m 6g --target $(NAME_ECHO_SERVER) .

.PHONY: push-echo-server-image
push-echo-server-image:
	@docker push $(ECHO_SERVER_IMAGE):$(ECHO_SERVER_VERSION)
	@docker push $(ECHO_SERVER_IMAGE):latest

#####################################################################
# Rules for verification, formatting, linting, testing and cleaning #
#####################################################################

.PHONY: tidy
tidy:
	@GO111MODULE=on go mod tidy
	@GARDENER_HACK_DIR=$(REPO_TOOLS_HACK_DIR) bash $(HACK_DIR)/update-github-templates.sh
	@cp $(REPO_TOOLS_HACK_DIR)/cherry-pick-pull.sh $(HACK_DIR)/cherry-pick-pull.sh && chmod +xw $(HACK_DIR)/cherry-pick-pull.sh

.PHONY: clean
clean:
	@$(shell find ./example -type f -name "controller-registration.yaml" -exec rm '{}' \;)
	@$(shell find ./example -type f -name "extension.yaml" -exec rm '{}' \;)
	@bash $(REPO_TOOLS_HACK_DIR)/clean.sh ./cmd/... ./pkg/... ./test/...

.PHONY: check-generate
check-generate:
	@bash $(REPO_TOOLS_HACK_DIR)/check-generate.sh $(REPO_ROOT)

.PHONY: check
check: $(GOIMPORTS) $(GOLANGCI_LINT) $(HELM) $(YQ) $(LOGCHECK)
	@sed ./.golangci.yaml.in -e "s#<<LOGCHECK_PLUGIN_PATH>>#$(TOOLS_BIN_DIR)#g" > ./.golangci.yaml
	@REPO_ROOT=$(REPO_ROOT) bash $(REPO_TOOLS_HACK_DIR)/check.sh --golangci-lint-config=./.golangci.yaml ./cmd/... ./pkg/... ./test/...
	@REPO_ROOT=$(REPO_ROOT) bash $(REPO_TOOLS_HACK_DIR)/check-charts.sh ./charts
	@GARDENER_HACK_DIR=$(REPO_TOOLS_HACK_DIR) $(HACK_DIR)/check-skaffold-deps.sh

tools-for-generate: $(CONTROLLER_GEN) $(EXTENSION_GEN) $(GEN_CRD_API_REFERENCE_DOCS) $(GOIMPORTS) $(HELM) $(KUSTOMIZE) $(YQ)
	@go mod download

.PHONY: generate
generate: tools-for-generate
	@REPO_ROOT=$(REPO_ROOT) GARDENER_HACK_DIR=$(REPO_TOOLS_HACK_DIR) bash $(REPO_TOOLS_HACK_DIR)/generate-sequential.sh ./charts/... ./cmd/... ./example/... ./imagevector/... ./pkg/... ./test/...
	@REPO_ROOT=$(REPO_ROOT) GARDENER_HACK_DIR=$(REPO_TOOLS_HACK_DIR) $(HACK_DIR)/update-codegen.sh
	$(MAKE) format

.PHONY: generate-extension
generate-extension: $(EXTENSION_GEN) $(KUSTOMIZE)
	@bash $(REPO_TOOLS_HACK_DIR)/generate-sequential.sh ./example/...

.PHONY: generate-controller-registration
generate-controller-registration:
	@bash $(REPO_TOOLS_HACK_DIR)/generate-sequential.sh ./charts/...

.PHONY: format
format: $(GOIMPORTS) $(GOIMPORTSREVISER)
	@bash $(REPO_TOOLS_HACK_DIR)/format.sh ./cmd ./pkg ./test

.PHONY: sast
sast: $(GOSEC)
	@bash $(REPO_TOOLS_HACK_DIR)/sast.sh --exclude-dirs hack,gardener

.PHONY: sast-report
sast-report: $(GOSEC)
	@bash $(REPO_TOOLS_HACK_DIR)/sast.sh --exclude-dirs hack,gardener --gosec-report true

.PHONY: test
test: $(REPORT_COLLECTOR)
	@bash $(REPO_TOOLS_HACK_DIR)/test.sh ./cmd/... ./pkg/...

.PHONY: test-integration
test-integration: $(REPORT_COLLECTOR) $(SETUP_ENVTEST)
	@bash $(GARDENER_HACK_DIR)/test-integration.sh ./test/integration/...

.PHONY: test-cov
test-cov:
	@bash $(REPO_TOOLS_HACK_DIR)/test-cover.sh ./cmd/... ./pkg/...

.PHONY: test-clean
test-clean:
	@bash $(REPO_TOOLS_HACK_DIR)/test-cover-clean.sh

.PHONY: verify
verify: check format test sast

.PHONY: verify-extended
verify-extended: check-generate check format test test-cov test-clean sast-report

test-e2e-local: $(GINKGO)
	./hack/test-e2e-local.sh --procs=$(PARALLEL_E2E_TESTS) ./test/e2e/...

ci-e2e-kind: $(KIND) $(YQ)
	./hack/ci-e2e-kind.sh

# speed-up skaffold deployments by building all images concurrently
export SKAFFOLD_BUILD_CONCURRENCY = 0
# build the images for the platform matching the nodes of the active kubernetes cluster, even in `skaffold build`, which doesn't enable this by default
export SKAFFOLD_CHECK_CLUSTER_NODE_PLATFORMS = true
export SKAFFOLD_DEFAULT_REPO = registry.local.gardener.cloud:5001
export SKAFFOLD_PUSH = true
# use static label for skaffold to prevent rolling all gardener components on every `skaffold` invocation
export SKAFFOLD_LABEL = skaffold.dev/run-id=extension-local

extension-up: $(SKAFFOLD) $(HELM) $(KUBECTL) $(KIND)
	@LD_FLAGS=$(LD_FLAGS) $(SKAFFOLD) run

extension-dev: $(SKAFFOLD) $(HELM) $(KUBECTL) $(KIND)
	$(SKAFFOLD) dev --cleanup=false --trigger=manual

extension-down: $(SKAFFOLD) $(HELM) $(KUBECTL)
	$(SKAFFOLD) delete
	@# The validating webhook is not part of the chart but it is created on admission Pod startup. Hence, we have to delete it explicitly.
	$(KUBECTL) delete validatingwebhookconfiguration gardener-extension-shoot-rsyslog-relp-admission --ignore-not-found

remote-extension-up remote-extension-down: export SKAFFOLD_LABEL = skaffold.dev/run-id=extension-remote

remote-extension-up: $(SKAFFOLD) $(HELM) $(KUBECTL) $(YQ)
	@LD_FLAGS=$(LD_FLAGS) ./hack/remote-extension-up.sh --path-seed-kubeconfig $(SEED_KUBECONFIG)

remote-extension-down: $(SKAFFOLD) $(HELM) $(KUBECTL)
	$(SKAFFOLD) delete -m admission,extension
	@# The validating webhook is not part of the chart but it is created on admission Pod startup. Hence, we have to delete it explicitly.
	$(KUBECTL) delete validatingwebhookconfiguration gardener-extension-shoot-rsyslog-relp-admission --ignore-not-found

configure-shoot: $(HELM) $(KUBECTL) $(YQ)
	@./hack/configure-shoot.sh --shoot-name $(SHOOT_NAME) --shoot-namespace $(SHOOT_NAMESPACE) --echo-server-image "$(ECHO_SERVER_IMAGE):$(ECHO_SERVER_VERSION)"
