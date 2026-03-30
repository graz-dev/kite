# ─── Configuration ───────────────────────────────────────────────────────────

# Image / release settings
IMAGE_REGISTRY ?= ghcr.io
IMAGE_REPO     ?= graz-dev/kite
VERSION        ?= dev
IMG            ?= $(IMAGE_REGISTRY)/$(IMAGE_REPO):$(VERSION)

# Tools
LOCALBIN       ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST        ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT  ?= $(LOCALBIN)/golangci-lint

# Controller-gen version pinned for reproducible builds
CONTROLLER_TOOLS_VERSION ?= v0.16.4
GOLANGCI_LINT_VERSION    ?= v2.11.4

# ─── Default target ──────────────────────────────────────────────────────────

.PHONY: all
all: generate manifests build

# ─── Build ───────────────────────────────────────────────────────────────────

.PHONY: build
build: ## Build the operator binary
	go build -ldflags="-s -w -X main.version=$(VERSION)" -o bin/kite-operator ./cmd/main.go

.PHONY: run
run: manifests generate ## Run the operator against the currently configured cluster (for local dev)
	POD_NAMESPACE=kite-system go run ./cmd/main.go \
		--leader-elect=false \
		--zap-log-level=debug

.PHONY: test
test: manifests generate envtest ## Run unit and integration tests
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-path $(LOCALBIN) -p path)" \
		go test ./... -v -coverprofile cover.out

.PHONY: lint
lint: golangci-lint ## Lint the source code
	$(GOLANGCI_LINT) run ./...

.PHONY: fmt
fmt: ## Format Go source with gofmt
	gofmt -w -s .

.PHONY: vet
vet: ## Run go vet
	go vet ./...

# ─── Docker ──────────────────────────────────────────────────────────────────

.PHONY: docker-build
docker-build: ## Build the Docker image
	docker build \
		--build-arg VERSION=$(VERSION) \
		-t $(IMG) \
		.

.PHONY: docker-push
docker-push: ## Push the Docker image
	docker push $(IMG)

.PHONY: docker-buildx
docker-buildx: ## Build and push a multi-arch image via buildx
	docker buildx build \
		--platform linux/amd64,linux/arm64 \
		--build-arg VERSION=$(VERSION) \
		-t $(IMG) \
		--push \
		.

# ─── Code generation ─────────────────────────────────────────────────────────

.PHONY: generate
generate: controller-gen ## Regenerate deepcopy functions
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: manifests
manifests: controller-gen ## Regenerate CRD YAML manifests and RBAC
	$(CONTROLLER_GEN) \
		rbac:roleName=kite-manager-role \
		crd \
		webhook \
		paths="./..." \
		output:crd:artifacts:config=config/crd/bases \
		output:rbac:artifacts:config=config/rbac

# ─── Deployment ──────────────────────────────────────────────────────────────

.PHONY: install
install: manifests ## Install CRDs into the cluster
	kubectl apply -f config/crd/bases/

.PHONY: uninstall
uninstall: ## Uninstall CRDs from the cluster
	kubectl delete --ignore-not-found -f config/crd/bases/

.PHONY: deploy
deploy: manifests ## Deploy the operator to the cluster
	kubectl apply -k config/default/

.PHONY: undeploy
undeploy: ## Remove the operator from the cluster
	kubectl delete --ignore-not-found -k config/default/

.PHONY: create-namespace
create-namespace: ## Create the kite-system namespace
	kubectl create namespace kite-system --dry-run=client -o yaml | kubectl apply -f -

# ─── Tool installation ───────────────────────────────────────────────────────

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(CONTROLLER_GEN) || \
		GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest binaries
$(ENVTEST): $(LOCALBIN)
	test -s $(ENVTEST) || \
		GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary
$(GOLANGCI_LINT): $(LOCALBIN)
	test -s $(GOLANGCI_LINT) || \
		GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# ─── Docs ────────────────────────────────────────────────────────────────────

.PHONY: docs-serve
docs-serve: ## Serve public documentation locally with MkDocs
	cd docs/public && pip install mkdocs-material && mkdocs serve

.PHONY: docs-build
docs-build: ## Build the public docs static site
	cd docs/public && mkdocs build

# ─── Help ────────────────────────────────────────────────────────────────────

.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} \
		/^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-26s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
