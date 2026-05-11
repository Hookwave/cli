# Convenience targets for local CLI development.
#
# Common flows:
#   make            # build a dev binary at ./bin/hookwave
#   make test       # run all Go tests
#   make snapshot   # produce release artifacts in ./dist without publishing
#   make install    # build and copy to /usr/local/bin/hookwave

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
  -X main.version=$(VERSION) \
  -X main.commit=$(COMMIT) \
  -X main.date=$(DATE)

.PHONY: all build test vet fmt clean snapshot install

all: build

build:
	@mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/hookwave ./cmd/hookwave

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w -s .

clean:
	rm -rf bin dist

# Build cross-platform release artifacts in ./dist without publishing.
# Requires `goreleaser` on PATH (https://goreleaser.com).
snapshot:
	goreleaser release --snapshot --clean --skip publish

install: build
	install -m 755 bin/hookwave /usr/local/bin/hookwave
