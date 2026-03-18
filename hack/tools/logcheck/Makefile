# Makefile for logcheck plugin and binary

BINARY_NAME=logcheck
PLUGIN_NAME=logcheck-plugin
PKG_PATH=hack/tools/logcheck/pkg/logcheck
PLUGIN_PATH=hack/tools/logcheck/plugin

.PHONY: build build-plugin test clean install verify

# Build the standalone binary
build:
	go build -o bin/logcheck ./pkg/logcheck

# Build the golangci-lint plugin
build-plugin:
	CGO_ENABLED=1 go build -o bin/logcheck.so -buildmode=plugin ./plugin

# Run tests
test:
	go test -v ./pkg/logcheck ./plugin

# Clean build artifacts
clean:
	rm -rf bin/

# Install the tool
install:
	go install ./pkg/logcheck

# Verify dependencies
verify:
	go mod verify
	go mod tidy -diff
