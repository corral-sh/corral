MODULE   := github.com/corral-sh/corral
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
  -X $(MODULE)/internal/cli.Version=$(VERSION) \
  -X $(MODULE)/internal/cli.Commit=$(COMMIT) \
  -X $(MODULE)/internal/cli.Date=$(DATE)
PREFIX   ?= $(shell if [ -w /opt/homebrew/bin ]; then echo /opt/homebrew; else echo $(HOME)/.local; fi)
BIN      := bin/corral

.PHONY: build install test vet lint fmt security dist clean run e2e site docs

build: ## Build ./bin/corral for this machine
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/corral

install: build ## Install into $(PREFIX)/bin
	install -d $(PREFIX)/bin
	install -m 0755 $(BIN) $(PREFIX)/bin/corral
	@echo "installed $(PREFIX)/bin/corral ($(VERSION))"

test: ## Run unit tests
	go test ./...

vet: ## go vet
	go vet ./...

fmt: ## gofmt check
	@test -z "$$(gofmt -l cmd internal tools | tee /dev/stderr)" || (echo 'run gofmt -w .' && exit 1)

lint: vet fmt ## vet + fmt + golangci-lint (incl. gosec)
	golangci-lint run ./...

security: lint ## Everything the CI security stage runs: lint, govulncheck, shellcheck, gitleaks
	govulncheck ./...
	shellcheck -S warning install.sh scripts/*.sh internal/guest/scripts/*.sh
	gitleaks git --no-banner --redact .

e2e: build ## End-to-end on this Mac: throwaway boxes under a temp CORRAL_HOME, guest assertions, cleanup (~4 min)
	scripts/e2e.sh

docs: ## Regenerate docs/FEATURES.md (the feature catalog) from the code; a test fails when it is stale
	go run ./cmd/corral docs > docs/FEATURES.md

site: ## Render the project site (GitHub Pages) into public/ from README, docs and changelog
	go run ./tools/site -out public -version "$(VERSION)"

dist: ## Cross-compile release binaries into dist/
	rm -rf dist && mkdir -p dist
	for arch in arm64 amd64; do \
	  CGO_ENABLED=0 GOOS=darwin GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o dist/corral-darwin-$$arch ./cmd/corral; \
	done
	cd dist && shasum -a 256 corral-* > SHA256SUMS
	@ls -la dist

clean:
	rm -rf bin dist

help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'
