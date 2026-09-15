# kei-cli release targets.
#
# Prerequisites: Go 1.26+, goreleaser (for release targets).
#
# Required environment for release:
#   GORELEASER_KEY        — GPG key ID for signing
#   AWS_S3_RELEASES_BUCKET — S3 bucket name
#   AWS_S3_RELEASES_REGION — AWS region
#   AWS_ACCESS_KEY_ID      — AWS access key (or IAM role)
#   AWS_SECRET_ACCESS_KEY  — AWS secret key
#
# See .goreleaser.yaml for full configuration details.

GO       ?= go
BINARY   ?= kei
VERSION  ?= dev
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  ?= -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: default build build-all test test-race clean release snapshot check

default: build

build:
	$(GO) build -o tmp/$(BINARY) -ldflags='$(LDFLAGS)' -trimpath .

build-all:
	GOOS=darwin GOARCH=amd64 $(GO) build -o tmp/kei-darwin-amd64 -ldflags='$(LDFLAGS)' -trimpath .
	GOOS=darwin GOARCH=arm64 $(GO) build -o tmp/kei-darwin-arm64 -ldflags='$(LDFLAGS)' -trimpath .
	GOOS=linux GOARCH=amd64 $(GO) build -o tmp/kei-linux-amd64 -ldflags='$(LDFLAGS)' -trimpath .
	GOOS=linux GOARCH=arm64 $(GO) build -o tmp/kei-linux-arm64 -ldflags='$(LDFLAGS)' -trimpath .

test:
	$(GO) test -p 1 -count=1 ./...

test-race:
	$(GO) test -p 1 -race -count=1 ./...

clean:
	rm -rf tmp/ dist/

release:
	goreleaser release --clean

snapshot:
	goreleaser release --snapshot --clean

check:
	goreleaser check --config .goreleaser.yaml
