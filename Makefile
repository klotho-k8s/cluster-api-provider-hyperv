# Makefile for cluster-api-provider-hyperv.
#
# controller-gen and kustomize are invoked with `go run` at pinned versions, so
# no separately installed kubebuilder/controller-gen is required.

CONTROLLER_GEN_VER ?= v0.19.0
KUSTOMIZE_VER ?= v5.8.1
CONTROLLER_GEN = go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VER)
KUSTOMIZE = go run sigs.k8s.io/kustomize/kustomize/v5@$(KUSTOMIZE_VER)

.PHONY: all
all: generate manifests fmt vet build test

.PHONY: generate
generate: ## Generate DeepCopy methods.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/v1alpha1"

.PHONY: manifests
manifests: ## Generate CRDs and RBAC.
	$(CONTROLLER_GEN) crd paths="./api/v1alpha1" output:crd:artifacts:config=config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=manager-role paths="./internal/controller/..." output:rbac:artifacts:config=config/rbac

.PHONY: fmt
fmt: ## Run gofmt.
	gofmt -w .

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: build
build: ## Build the manager binary.
	go build -o bin/manager ./cmd

.PHONY: agent
agent: ## Build the Hyper-V host agent (windows/amd64).
	GOOS=windows GOARCH=amd64 go build -o bin/hyperv-agent.exe ./cmd/hyperv-agent

.PHONY: proto
proto: ## Regenerate the gRPC stubs from proto/ (needs protoc + plugins).
	protoc --proto_path=proto \
		--go_out=. --go_opt=module=github.com/klotho-k8s/cluster-api-provider-hyperv \
		--go-grpc_out=. --go-grpc_opt=module=github.com/klotho-k8s/cluster-api-provider-hyperv \
		proto/hypervagent/v1/agent.proto

.PHONY: test
test: ## Run unit tests.
	go test ./... -count=1

.PHONY: release-manifests
release-manifests: manifests ## Render the infrastructure-components manifest.
	$(KUSTOMIZE) build config/default > infrastructure-components.yaml
