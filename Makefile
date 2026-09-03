BIN := build/magicmarkets

# The main package lives in cmd/magicmarkets so the binary is named `magicmarkets`. Building
# the module root instead would name it after the module path — magicmarkets-cli — which
# is not what any of the docs or `magicmarkets --help` tell you to run.
PKG := ./cmd/magicmarkets

# Stamped into the binary as main.version. Overridable: make build VERSION=v1.2.3
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -trimpath -ldflags "-s -w -X main.version=$(VERSION)"

SPEC     := internal/spec/openapi.json
DOCS     := docs/api-reference.md
PREPARED := build/openapi-prepared.json

# The canonical spec and reference, as linked from https://magicmarkets.com/magic-api/docs.
# Every path also works without the /magic-api ingress prefix.
API      := https://magicmarkets.com/magic-api/v2
DOCS_URL := https://magicmarkets.com/magic-api/docs

.PHONY: build install where test lint fmt clean generate update-spec release help

help:
	@echo "make build         build ./$(BIN)"
	@echo "make install       install the magicmarkets binary into GOBIN"
	@echo "make where         show where make install puts the binary"
	@echo "make test          run the test suite"
	@echo "make lint          go vet"
	@echo "make fmt           gofmt -w"
	@echo "make generate      regenerate internal/magicmarketsapi from the vendored spec"
	@echo "make update-spec   refresh the vendored OpenAPI spec and docs, then regenerate"
	@echo "make release VERSION=v1.0.0   cross-compile and publish a GitHub release"

build:
	@mkdir -p $(dir $(BIN))
	go build $(LDFLAGS) -o $(BIN) $(PKG)
	@echo "built $(BIN) ($(VERSION)) — run it with ./$(BIN)"

# GOBIN wins if set, otherwise go installs into GOPATH/bin.
GOBIN_DIR := $(shell go env GOBIN)
ifeq ($(GOBIN_DIR),)
GOBIN_DIR := $(shell go env GOPATH)/bin
endif

install:
	go install $(LDFLAGS) $(PKG)
	@echo "installed $(GOBIN_DIR)/magicmarkets ($(VERSION))"
	@command -v magicmarkets >/dev/null 2>&1 \
		|| echo "warning: $(GOBIN_DIR) is not on your PATH — add it, or run $(GOBIN_DIR)/magicmarkets directly"

where:
	@echo "$(GOBIN_DIR)/magicmarkets"

test:
	go test ./...

lint:
	go vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './build/*')

clean:
	rm -rf build dist

# Regenerate the spec-derived models in internal/magicmarketsapi.
#
# prepspec adapts the vendored spec for oapi-codegen (number -> float64 so money
# keeps precision, and StakeTuple flattened because an OpenAPI 3.1 tuple cannot
# be generated). oapi-codegen is pinned by the tool directive in go.mod, so this
# needs no separate install.
#
# The generated file is checked in: a plain `git clone && go build` must work
# without running codegen.
generate:
	@mkdir -p $(dir $(PREPARED))
	go run ./tools/prepspec -in $(SPEC) -out $(PREPARED)
	go tool oapi-codegen -config internal/magicmarketsapi/oapi-codegen.yaml $(PREPARED)
	gofmt -w internal/magicmarketsapi/types.gen.go
	@rm -f $(PREPARED)
	@echo "regenerated internal/magicmarketsapi/types.gen.go"

# Refresh the vendored API contract, then regenerate from it.
#
# The spec is embedded in the binary, so the `magicmarkets api` commands only know what
# is checked in here. Run `git diff` afterwards to see what the API changed;
# contract_test.go will fail if a change affects a type the client relies on.
update-spec:
	curl -sSf $(API)/openapi.json -o $(SPEC)
	curl -sSf -H 'Accept: text/markdown' $(DOCS_URL) -o $(DOCS)
	@echo "updated $(SPEC) and $(DOCS)"
	$(MAKE) generate
	go build ./... && go test ./...

# Build and publish a release. Requires the gh CLI (brew install gh, gh auth login).
# Usage: make release VERSION=v1.0.0
#
# VERSION must be passed explicitly here. It defaults to `git describe` for local
# builds, which would otherwise publish a release named after a bare commit.
release:
	@case "$(VERSION)" in v*) ;; *) echo "Usage: make release VERSION=v1.0.0" && exit 1 ;; esac
	@command -v gh >/dev/null 2>&1 || (echo "Need gh CLI: brew install gh && gh auth login" && exit 1)
	@mkdir -p dist && rm -f dist/magicmarkets_*
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o dist/magicmarkets_$(VERSION)_linux_amd64 $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o dist/magicmarkets_$(VERSION)_linux_arm64 $(PKG)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o dist/magicmarkets_$(VERSION)_darwin_amd64 $(PKG)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o dist/magicmarkets_$(VERSION)_darwin_arm64 $(PKG)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o dist/magicmarkets_$(VERSION)_windows_amd64.exe $(PKG)
	gh release create $(VERSION) dist/magicmarkets_* --generate-notes
	@echo "Release $(VERSION) created."
