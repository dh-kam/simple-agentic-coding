# agentic — build & cross-compile release binaries.
# Usage:
#   make build            # local binary → dist/agentic
#   make release          # cross-compile all release targets → dist/
#   make release-darwin-arm64   # one target
#   make clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell git log -1 --format=%cI 2>/dev/null || echo unknown)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
DIST := dist

.PHONY: all build release clean

all: build

build:
	mkdir -p $(DIST)
	go build -ldflags "$(LDFLAGS)" -o $(DIST)/agentic .

# All cross-compiled release binaries (static, CGO disabled).
release: release-linux-amd64 release-linux-arm64 release-darwin-amd64 release-darwin-arm64 release-windows-amd64
	@ls -lh $(DIST)

release-linux-amd64:
	mkdir -p $(DIST)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(DIST)/agentic-linux-amd64 .

release-linux-arm64:
	mkdir -p $(DIST)
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(DIST)/agentic-linux-arm64 .

release-darwin-amd64:
	mkdir -p $(DIST)
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(DIST)/agentic-darwin-amd64 .

release-darwin-arm64:
	mkdir -p $(DIST)
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(DIST)/agentic-darwin-arm64 .

release-windows-amd64:
	mkdir -p $(DIST)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(DIST)/agentic-windows-amd64.exe .

clean:
	rm -rf $(DIST)
