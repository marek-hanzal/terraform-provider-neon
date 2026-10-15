NAMESPACE ?= marek-hanzal
NAME := neon
VERSION ?= 0.17.0-serakki.3
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
OS_ARCH := $(GOOS)_$(GOARCH)
BINARY := terraform-provider-$(NAME)_v$(VERSION)
BUILD_DIR ?= build
MIRROR := $(abspath $(BUILD_DIR)/mirror)
INSTALL_DIR := $(MIRROR)/registry.terraform.io/$(NAMESPACE)/$(NAME)/$(VERSION)/$(OS_ARCH)
LDFLAGS := -s -w -X main.version=$(VERSION) -X github.com/neondatabase/terraform-provider-neon/provider.Name=$(NAMESPACE)/$(NAME)

.PHONY: help build install test testacc docu lint crossbuild

help: ## Show local commands. Run through mise exec or mise run.
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "%-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build a reproducible provider binary for GOOS/GOARCH.
	@mkdir -p $(BUILD_DIR)/$(OS_ARCH)
	CGO_ENABLED=0 GOTOOLCHAIN=local GOOS=$(GOOS) GOARCH=$(GOARCH) go build -mod=readonly -trimpath -buildvcs=false -ldflags='$(LDFLAGS)' -o $(BUILD_DIR)/$(OS_ARCH)/$(BINARY)$(if $(filter windows,$(GOOS)),.exe,) .

install: build ## Install into the checkout's filesystem mirror; write its CLI config.
	@mkdir -p $(INSTALL_DIR)
	cp $(BUILD_DIR)/$(OS_ARCH)/$(BINARY)$(if $(filter windows,$(GOOS)),.exe,) $(INSTALL_DIR)/
	@printf 'provider_installation {\n  filesystem_mirror {\n    path = "$(MIRROR)"\n    include = ["registry.terraform.io/$(NAMESPACE)/$(NAME)"]\n  }\n  direct {\n    exclude = ["registry.terraform.io/$(NAMESPACE)/$(NAME)"]\n  }\n}\n' > $(BUILD_DIR)/terraform.rc
	@printf 'TF_CLI_CONFIG_FILE=%s\n' '$(abspath $(BUILD_DIR)/terraform.rc)'

crossbuild: ## Build and install supported darwin/linux/windows amd64/arm64 binaries.
	@for platform in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64 windows_amd64 windows_arm64; do \
		$(MAKE) install GOOS=$${platform%_*} GOARCH=$${platform#*_} || exit 1; \
	done

test: ## Run unit and mocked Terraform lifecycle tests without Neon credentials.
	go test ./...

testacc: ## Run the existing live suite (creates real resources).
	TF_ACC=1 go test -tags=acceptance -v -timeout 120m ./...

docu: ## Format examples and generate registry documentation.
	go generate ./...

lint: ## Run the pinned linter and fail on findings.
	golangci-lint run --timeout 3m
