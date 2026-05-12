GO_PACKAGES := ./...
GOFMT_DIRS  := cmd internal

# VERSION stamps the binary via -ldflags="-X main.version=...".
# Defaults to `git describe` so dev builds carry a meaningful identifier
# without a manual override. CI/release builds pass VERSION=vX.Y.Z explicitly.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO_LDFLAGS := -s -w -X main.version=$(VERSION)

# golangci-lint resolution order: PATH → $GOBIN → $GOPATH/bin.
GOLANGCI_LINT ?= $(shell if command -v golangci-lint >/dev/null 2>&1; then \
		command -v golangci-lint; \
	else \
		GOBIN=$$(go env GOBIN); GOPATH=$$(go env GOPATH); \
		if [ -n "$$GOBIN" ] && [ -x "$$GOBIN/golangci-lint" ]; then \
			printf "%s/golangci-lint" "$$GOBIN"; \
		elif [ -x "$$GOPATH/bin/golangci-lint" ]; then \
			printf "%s/bin/golangci-lint" "$$GOPATH"; \
		fi; \
	fi)

DEVSERVER_IMAGE      ?= qbit-mcp-devserver
# Ports offset from dmhy-mcp devserver (8090 + 6374/6377) and kura devserver
# (8080/8081 + 6274/6277) so all three can run concurrently. Override on the
# make command line if these collide too.
MCP_DEV_PORT         ?= 8091
INSPECTOR_PORT       ?= 6474
INSPECTOR_PROXY_PORT ?= 6477

.PHONY: build check fmt lint test vet devserver-build devserver-run

build:
	go build -trimpath -ldflags='$(GO_LDFLAGS)' -o bin/qbit-mcp ./cmd/qbit-mcp

fmt:
	gofmt -w $(GOFMT_DIRS)

vet:
	go vet $(GO_PACKAGES)

lint:
	@if [ -z "$(GOLANGCI_LINT)" ]; then \
		echo "golangci-lint not found. Install it with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
		exit 127; \
	fi
	$(GOLANGCI_LINT) run $(GO_PACKAGES)

test:
	go test $(GO_PACKAGES)

check: fmt vet lint test build

devserver-build:
	docker build -f tools/devserver/Dockerfile -t $(DEVSERVER_IMAGE) .

# Forwards QBITTORRENT_URL, QBITTORRENT_LOG_LEVEL, MCP_PROXY_AUTH_TOKEN from
# the host shell into the container when set. All host-side port binds pin
# to 127.0.0.1 so they are not reachable from the network.
#
# To develop against qBittorrent running on the host, set
#   QBITTORRENT_URL=http://host.docker.internal:8080
# in your shell before invoking this target.
devserver-run:
	docker run --rm -it \
		-p 127.0.0.1:$(MCP_DEV_PORT):8091 \
		-p 127.0.0.1:$(INSPECTOR_PORT):6474 \
		-p 127.0.0.1:$(INSPECTOR_PROXY_PORT):6477 \
		-v "$(CURDIR):/src" \
		$(if $(QBITTORRENT_URL),-e QBITTORRENT_URL="$(QBITTORRENT_URL)") \
		$(if $(QBITTORRENT_LOG_LEVEL),-e QBITTORRENT_LOG_LEVEL="$(QBITTORRENT_LOG_LEVEL)") \
		$(if $(MCP_PROXY_AUTH_TOKEN),-e MCP_PROXY_AUTH_TOKEN="$(MCP_PROXY_AUTH_TOKEN)") \
		$(DEVSERVER_IMAGE)
