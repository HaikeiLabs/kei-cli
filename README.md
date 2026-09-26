# Kei CLI

The Kei CLI manages Kei bot installation metadata. It authenticates operators,
creates an installation for a bot platform, assigns agents, and reports the
installation status. Runtime deployment and credentials are managed outside
this CLI.

## Install

Binary releases of kei-cli are available for macOS and Linux (arm64 and amd64).
The installer downloads the matching archive and verifies its SHA-256 checksum.

### Option 1: curl install from S3 (recommended)

The public release endpoint is an S3 website endpoint. The install script
detects your OS and architecture and defaults to `/usr/local/bin`:

```sh
# Install the latest version to a user-writable directory:
export PATH="$HOME/.local/bin:$PATH"
curl -fsSL "https://kei-cli-releases.s3.us-east-1.amazonaws.com/kei-cli/install.sh" \
  | bash -s -- -d "$HOME/.local/bin"

# Install a specific version:
curl -fsSL "https://kei-cli-releases.s3.us-east-1.amazonaws.com/kei-cli/install.sh" \
  | bash -s -- -v 0.2.0 -d "$HOME/.local/bin"
```

After installing, verify:

```sh
kei help
kei --version
```

### Option 2: go install

Requires Go 1.26+:

```sh
go install github.com/HaikeiLabs/kei-cli/cmd/kei@latest
export PATH="$(go env GOPATH)/bin:$PATH"
```

Add the export line to `~/.zshrc` (or your shell's startup file) to make it
permanent.

To upgrade from the release endpoint, rerun the installer command. Pass
`-v VERSION` when a pinned release is required. The built-in Go-module upgrade
path is also available when Go is installed:

```sh
kei upgrade
kei upgrade --version VERSION
```

### Building from source

```sh
go build -o tmp/kei ./cmd/kei/
./tmp/kei help
```

## Configure a local runtime

To run a local headless harness against an existing Kei runtime installation,
run setup once:

```sh
kei setup
```

Setup prompts for the production control-plane URL, runtime installation token,
local harness URL, `kei-proxy` path, and optional model settings. It verifies the
token with Kei before saving the configuration to `~/.config/kei.yaml`. The file
is created with `0600` permissions and the token is never printed. For scripted
or non-interactive setup, pass the token explicitly (be aware it may be saved
in shell history):

```sh
kei setup \
  --control-plane-url https://app.haikeilabs.com \
  --runtime-token RUNTIME_INSTALLATION_TOKEN
```

Verify the saved runtime installation and send its heartbeat through the local
`kei-proxy` binary:

```sh
kei runtime bootstrap
```

Use `--config PATH` if you need a separate configuration file. The runtime
token is used only by the local proxy; it is not sent to the model or written
to the repository.

## Login

The CLI uses Kei device authorization. It prints a browser URL and one-time
verification code; complete the flow in your browser:

```sh
kei login
```

The approval page opens automatically. Use `--no-browser` in a headless
environment.

The login token is stored in the operating system credential store. It is not
printed or written to the repository.

## Logout

Remove the stored CLI token for a Kei environment:

```sh
kei logout
```

Logout only removes the local credential from the OS keychain; it does not
revoke the token server-side. It is idempotent: running it while not logged
in succeeds and reports that no session was stored.

## Manage an installation

Create pending installation metadata for a bot:

```sh
kei bot init \
  --platform cli \
  --name "Local OpenWebUI" \
  --workspace "My Workspace"
```

Use `--platform cli` for a platform-neutral local runtime such as
`kei-connector-runtime` behind OpenWebUI. The existing `teams`, `discord`, and
`slack` values remain for chat-platform installations. The `--workspace` flag
is optional on `init`; when provided, the workspace ID or name is forwarded to
the installation record.

The command prints the installation ID. Use that ID to inspect the installation
or manage its agent assignments:

```sh
kei bot status --installation INSTALLATION_ID
kei bot delete --installation INSTALLATION_ID --yes
kei bot agents list --installation INSTALLATION_ID
kei bot agents add --installation INSTALLATION_ID --agent AGENT_ID
kei bot agents add --installation INSTALLATION_ID --agent AGENT_ID --default
kei bot agents remove --installation INSTALLATION_ID --agent AGENT_ID
```

To create or rotate a runtime credential for a command pipeline, stdout must
be redirected or piped; the CLI refuses to print a credential to an
interactive terminal:

```sh
kei bot credential --installation INSTALLATION_ID --workspace WORKSPACE_ID | secret-manager import
kei bot credential --installation INSTALLATION_ID --workspace WORKSPACE_ID --rotate | secret-manager import
```

The `--workspace` flag is required and accepts a UUID or an exact workspace
name. When a name is given, the CLI resolves it to a UUID by calling the
workspaces API. The workspace value can also be set via the `KEI_WORKSPACE_ID`
environment variable.

List available workspaces:

```sh
kei workspaces list
kei workspaces list --json
```

The CLI always targets the Kei production API at `https://app.haikeilabs.com`.
Set the `KEI_WEB_URL` environment variable to override the endpoint for
development and testing.

## Rotating a runtime credential into AWS Secrets Manager

When a runtime's Kubernetes secret is owned by an ExternalSecret that syncs
from AWS Secrets Manager, the authoritative store is AWS. Rotate the credential
and update the AWS secret in one pipeline:

```sh
kei bot credential --installation INSTALLATION_ID --rotate \
  | ./scripts/update-aws-secret.sh kei-discord kei_runtime_token
```

`scripts/update-aws-secret.sh` reads the token **only from stdin**, updates
exactly one JSON property (default `kei_runtime_token`) of the secret's
`SecretString` with `jq --rawfile`, and calls `aws secretsmanager
put-secret-value` using `file://` input. The token is never placed in argv,
stdout, stderr, a temp filename, or shell history, and all other JSON fields are
preserved. The second argument is the property name; omit it to use the default
`kei_runtime_token`.

**Preflight first — before you rotate.** The rotation happens in the CLI *before*
the script runs, so a failed destination check cannot prevent the rotation. If
the write fails, the one-time token is consumed and you must rotate again. Verify
the destination is healthy first:

```sh
# 1. Confirm the AWS secret exists and is a JSON object (do this BEFORE rotating).
aws secretsmanager get-secret-value --secret-id kei-discord \
  --query SecretString --output text | jq -e 'type == "object"'
# 2. Confirm you can write to it.
aws secretsmanager describe-secret --secret-id kei-discord
```

The script prints only `Updated property 'kei_runtime_token' on secret
'kei-discord'.` on success. After the AWS secret is updated, the ExternalSecret
syncs it to the cluster; restart the runtime pod if it does not pick up the new
value automatically.

### Key naming

The runtime reads the `KEI_RUNTIME_TOKEN` environment variable. In the
Discord/AWS deployment the token is stored in the AWS secret's lowercase
`kei_runtime_token` JSON property, which the ExternalSecret maps onto the
Kubernetes secret key the deployment consumes. Pass that property name as the
script's second argument (it is the default).

## Release process

Releases are built and published by `.github/workflows/release.yaml`. For
the step-by-step runbook (prerequisites, watching a run, and what to do when
a run is skipped or fails), see [docs/release.md](docs/release.md).

### How a release runs

The workflow has three jobs:

1. **Authorize manual release.** Runs only for `workflow_dispatch`. It
   fails unless the person dispatching has `admin` permission on the
   repository (a repository or organization owner). It is skipped on tag
   pushes.
2. **Validate tag.** Resolves the release tag and rejects anything that is
   not `vMAJOR.MINOR.PATCH[-prerelease]`. It sets the channel: `prerelease`
   for tags containing `-rc.`, `-alpha.`, or `-beta.`, and `stable`
   otherwise. On a dispatch it also fails if the calculated tag already
   exists.
3. **Release.** Runs only when Validate tag succeeded. On a dispatch it
   first creates and pushes the calculated tag. It then imports the GPG
   signing key, checks the runner's AWS identity and bucket access, confirms
   the pinned kei-proxy archive exists, and runs GoReleaser to build, sign,
   and upload the artifacts. After that it uploads `install.sh`, checks the
   published checksums and installer for leaked credentials, and, for stable
   releases only, writes `latest.txt`.

Validate tag and Release run on the self-hosted `kei-cli-release` runner. The
runner Pod gets its AWS identity from IRSA, a dedicated IAM role for the
release service account. No AWS credentials, OIDC role ARN, or
`configure-aws-credentials` step appear in the repository or the workflow.

### Cutting a release

**Tag push.** Tag a commit on `main` and push the tag:

```sh
git fetch origin
git tag vX.Y.Z origin/main
git push origin vX.Y.Z
```

Before #44, tag pushes silently skipped the Release job. Since #44, a tag
push releases.

**Manual dispatch.** Let the workflow calculate the next version from the
highest stable `vX.Y.Z` tag, then create and push that tag itself:

```sh
gh workflow run release.yaml -R HaikeiLabs/kei-cli -f bump=patch   # or minor, major
```

`bump` defaults to `patch`. A dispatch requires a repository or organization
owner. For anyone else, Authorize manual release fails and nothing is
released. A dispatch releases the head of the ref it is started on, which is
normally `main`.

### The `release` environment

The Release job runs in the `release` GitHub environment. The environment
holds the `GORELEASER_SIGNING_KEY` secret, the ASCII-armored GPG private key
for `kei-cli-releases@haikeilabs.com`. The environment has no required
reviewers and no branch policy. The owner check for a dispatch lives in the
workflow, not in the environment.

Optional repository variables. None are set today, so the defaults apply:

| Variable | Default | Purpose |
|---|---|---|
| `AWS_REGION` | `us-east-1` | Region of the releases bucket |
| `AWS_S3_RELEASES_BUCKET` | `kei-cli-releases` | Releases bucket |
| `KEI_PROXY_VERSION` | `v0.1.0` | kei-proxy version bundled into each archive |

### What gets published

Everything goes to `s3://kei-cli-releases/kei-cli/`. The public URL is
`https://kei-cli-releases.s3.us-east-1.amazonaws.com/kei-cli/`. Versioned
paths use the version without the leading `v`:

```
kei-cli/
  install.sh                          curl installer; overwritten every release, no-cache
  latest.txt                          latest stable version, e.g. 0.1.6; stable releases only
  <version>/
    kei-cli_<version>_macOS_arm64.tar.gz
    kei-cli_<version>_macOS_x86_64.tar.gz
    kei-cli_<version>_Linux_arm64.tar.gz
    kei-cli_<version>_Linux_x86_64.tar.gz
    kei-cli_<version>_checksums.txt   SHA-256 of every artifact
    kei-cli_<version>_checksums.txt.sig   armored GPG signature of the checksums
    kei-cli_<version>_source.tar.gz
```

Each archive contains `kei`, `LICENSE`, `README.md`, and the pinned `kei-proxy`
for the same platform. A prerelease publishes its `<version>/` directory and
`install.sh`, but leaves `latest.txt` alone. Install a prerelease with
`-v <version>`. GitHub Releases are disabled; S3 is the only distribution
channel. Public reads come from the bucket policy, not object ACLs.

### Verifying a release

```sh
BASE=https://kei-cli-releases.s3.us-east-1.amazonaws.com/kei-cli
curl -fsSL "$BASE/latest.txt"                      # stable releases: prints the new version
V=0.1.6                                            # the version you released
A="kei-cli_${V}_macOS_arm64.tar.gz"
curl -fsSLO "$BASE/$V/$A"
curl -fsSLO "$BASE/$V/kei-cli_${V}_checksums.txt"
shasum -a 256 -c --ignore-missing "kei-cli_${V}_checksums.txt"   # expect: <archive>: OK
```

On Linux, use `sha256sum -c --ignore-missing` instead. A fresh install with
the curl installer is also an end-to-end check, because it verifies the same
checksum. See [docs/release.md](docs/release.md#4-verify-the-release) for the
signature check.

### Upgrading from 0.1.5 or earlier (one time)

`kei upgrade` in 0.1.5 and earlier runs
`go install github.com/HaikeiLabs/kei-cli@<version>`. The entrypoint moved to
`cmd/kei` in 0.1.6 (#38), so that command no longer builds. Reinstall once,
using either method:

```sh
curl -fsSL "https://kei-cli-releases.s3.us-east-1.amazonaws.com/kei-cli/install.sh" \
  | bash -s -- -d "$HOME/.local/bin"
# or
go install github.com/HaikeiLabs/kei-cli/cmd/kei@<version>
```

From 0.1.6 onward, `kei upgrade` installs `github.com/HaikeiLabs/kei-cli/cmd/kei`
and works as documented above. The old `go install` path produced a binary
named `kei-cli`, so after reinstalling you can remove any leftover
`$(go env GOPATH)/bin/kei-cli`.

### Bundled kei-proxy

Each CLI archive bundles the kei-proxy binary for its platform, pinned by
`KEI_PROXY_VERSION`. During the release, `scripts/fetch-proxy.sh` downloads
`s3://kei-cli-releases/kei-proxy/<version>/kei-proxy_<version>_<OS>_<ARCH>.tar.gz`
using the runner's IRSA identity. `<OS>` is `macOS` or `Linux`, `<ARCH>` is
`x86_64` or `arm64`, and `<version>` has no leading `v`, even when the pin
does. The Release job fails early if that proxy release does not exist. To
bundle a newer proxy, set the `KEI_PROXY_VERSION` repository variable before
cutting the CLI release. The installer installs `kei-proxy` when the archive
contains it. Older archives without it still install normally.

For a local test build with no upload and no signing:

```sh
KEI_PROXY_VERSION=v0.1.0 \
AWS_S3_RELEASES_URL_BASE=https://kei-cli-releases.s3.us-east-1.amazonaws.com \
AWS_S3_RELEASES_BUCKET=kei-cli-releases AWS_S3_RELEASES_REGION=us-east-1 \
GORELEASER_SKIP_SIGN=1 make snapshot
```

Artifacts land in `dist/`. Run `make clean` to remove them.

## Scope

The CLI currently supports login and logout, installation metadata, agent
assignment, installation status, and self-upgrade for the Teams, Discord, and
Slack platform identifiers. Runtime deployment workflows are intentionally
outside the CLI's scope.
