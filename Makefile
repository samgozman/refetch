GO ?= go
BIN := build
VERSION ?= dev
# golangci-lint with the maxcomments plugin compiled in (see .custom-gcl.yml).
GCL := $(BIN)/custom-gcl

.PHONY: all build test cover cover-check lint lint-build fmt tidy notices clean

all: lint test build

build:
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o $(BIN)/refetch ./cmd/refetch

# Unit tests need no network: fetch tests use fake clients and loopback servers.
test:
	$(GO) test -race ./...

# -coverpkg credits code that other packages' tests exercise (cmd/refetch
# drives fetch, render and store). cover-check fails below COVERAGE_MIN.
COVERAGE_MIN := 85

cover:
	$(GO) test -race -coverpkg=./... -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

cover-check: cover
	@total=$$($(GO) tool cover -func=coverage.out | awk '/^total:/ {print $$3}' | tr -d '%'); \
	awk "BEGIN { exit !($$total >= $(COVERAGE_MIN)) }" || \
		{ echo "coverage $$total% is below the $(COVERAGE_MIN)% minimum"; exit 1; }

lint: $(GCL)
	$(GO) mod tidy -diff
	$(GCL) run ./...

fmt: $(GCL)
	$(GCL) fmt ./...

# Needs the stock golangci-lint on PATH; rebuilt when .custom-gcl.yml changes.
lint-build $(GCL): .custom-gcl.yml
	golangci-lint custom

tidy:
	$(GO) mod tidy

# THIRD_PARTY_NOTICES: the license text of Go and every module compiled into
# the binary (tls-client's BSD-4-Clause license requires it in releases).
notices:
	scripts/notices.sh > THIRD_PARTY_NOTICES

clean:
	rm -rf $(BIN) coverage.out
