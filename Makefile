# kei-cli release targets.
#
# Prerequisites: Go 1.26+, goreleaser (for release targets).
#
# Required environment for release:
#   GORELEASER_KEY             — GPG key ID for signing
#   AWS_S3_RELEASES_BUCKET     — S3 bucket name
#   AWS_S3_RELEASES_REGION     — AWS region
#   AWS_ACCESS_KEY_ID          — AWS access key (or IAM role)
#   AWS_SECRET_ACCESS_KEY      — AWS secret key
#
# In CI (GitHub Actions), credentials are obtained via OIDC federation:
#   - id-token: write permissions
#   - aws-actions/configure-aws-credentials with role-to-assume
#   - No static AWS credentials stored in the repository
#
# See .goreleaser.yaml and .github/workflows/release.yaml for details.

GO       ?= go
BINARY   ?= kei
VERSION  ?= dev
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  ?= -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: default build build-all test test-race clean release snapshot check test-release-ci sign-check

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

test-release-ci:
	$(GO) test -p 1 -count=1 -run 'TestRelease|TestGoreleaser|TestInstallScript' ./...

clean:
	rm -rf tmp/ dist/

release:
	goreleaser release --clean

snapshot:
	goreleaser release --snapshot --clean

check:
	goreleaser check --config .goreleaser.yaml

sign-check:
	@echo "Checking GPG signing key availability..."
	gpg --list-keys --keyid-format LONG "$(GORELEASER_KEY)" 2>/dev/null || \
	  echo "WARNING: GPG key '$(GORELEASER_KEY)' not found in local keyring. Import the signing key before release."
