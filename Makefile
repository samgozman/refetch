GO ?= go
BIN := bin
VERSION ?= dev

.PHONY: all build test cover lint fmt tidy notices clean

all: lint test build

build:
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o $(BIN)/refetch ./cmd/refetch

# Unit tests need no network: fetch tests use fake clients and loopback servers.
test:
	$(GO) test -race ./...

cover:
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

lint:
	$(GO) mod tidy -diff
	golangci-lint run ./...

fmt:
	golangci-lint fmt ./...

tidy:
	$(GO) mod tidy

# THIRD_PARTY_NOTICES: the license text of Go and every module compiled into
# the binary (tls-client's BSD-4-Clause license requires it in releases).
notices:
	scripts/notices.sh > THIRD_PARTY_NOTICES

clean:
	rm -rf $(BIN) coverage.out
