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
  --name "Local OpenWebUI"
```

Use `--platform cli` for a platform-neutral local runtime such as
`kei-connector-runtime` behind OpenWebUI. The existing `teams`, `discord`, and
`slack` values remain for chat-platform installations.

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

The `--workspace` flag is required and accepts a UUID. The workspace ID can
also be set via the `KEI_WORKSPACE_ID` environment variable.

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

### CI/CD workflow

Releases are built and published by a GitHub Actions workflow
(`.github/workflows/release.yaml`) triggered by pushing a semver tag
(`vX.Y.Z`). The workflow:

1. **Validates the tag** — rejects non-semver tags and determines the release
   channel (stable vs. prerelease).
2. **Assumes an AWS IAM role** via GitHub OIDC federation — no static AWS
   credentials are stored in the repository.
3. **Imports the GPG signing key** from the `GORELEASER_SIGNING_KEY` secret.
4. **Configures AWS credentials** using `aws-actions/configure-aws-credentials`
   with `role-to-assume` from the `AWS_ROLE_TO_ASSUME` variable.
5. **Runs Goreleaser** — builds signed binaries, archives, and SHA-256
   checksums, then uploads them to the S3 releases bucket.
6. **Verifies no credentials leaked** into artifacts.
7. **Publishes `latest.txt`** for stable releases (used by the install script
   to resolve the latest version).

### Required GitHub configuration

| Name | Type | Description |
|---|---|---|
| `AWS_ROLE_TO_ASSUME` | Variable | ARN of the IAM role for OIDC federation |
| `AWS_REGION` | Variable | AWS region of the releases bucket |
| `AWS_S3_RELEASES_BUCKET` | Variable | S3 bucket name for release artifacts |
| `GORELEASER_SIGNING_KEY` | Secret | GPG private key for checksum signing |

### Required AWS IAM trust policy

The OIDC role assumed by the release workflow must have a trust policy
similar to:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {
      "Federated": "arn:aws:iam::ACCOUNT:oidc-provider/token.actions.githubusercontent.com"
    },
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringLike": {
        "token.actions.githubusercontent.com:sub": "repo:HaikeiLabs/kei-cli:ref:refs/tags/v*"
      },
      "StringEquals": {
        "token.actions.githubusercontent.com:aud": "sts.amazonaws.com"
      }
    }
  }]
}
```

The role must also have `s3:PutObject` and `s3:ListBucket` permissions on
`arn:aws:s3:::BUCKET/kei-cli/*`.

### Manual release

To trigger a release from a local workstation (not CI), first create and push
a tag:

```sh
git tag v0.1.0
git push origin v0.1.0
```

Then push the tag — the CI workflow handles the rest.

For a local test build (no upload, no signing):

```sh
make snapshot
```

### S3 bucket architecture

Release artifacts are served from an S3 bucket with the following structure.
All objects are published by the release workflow on tag push:

```
s3://BUCKET/kei-cli/
  install.sh                  — curl installer (fixed path, overwritten each release)
  latest.txt                  — latest stable version (stable releases only)
  v0.1.0/
    kei-cli_v0.1.0_macOS_arm64.tar.gz
    kei-cli_v0.1.0_macOS_x86_64.tar.gz
    kei-cli_v0.1.0_Linux_arm64.tar.gz
    kei-cli_v0.1.0_Linux_x86_64.tar.gz
    kei-cli_v0.1.0_checksums.txt
    kei-cli_v0.1.0_source.tar.gz
```

The `install.sh` script is uploaded to the fixed path `kei-cli/install.sh`
by the release workflow (not by Goreleaser, which only uploads versioned
artifacts). It is overwritten on every release with `--cache-control no-cache`
and `--acl public-read`.

The bucket must be publicly readable for object GETs (or fronted by a CDN).
The `install.sh` script constructs download URLs from
`AWS_S3_RELEASES_URL_BASE`.

### Bundled kei-proxy release input

Release archives bundle a pinned, platform-matched `kei-proxy` binary. The
pin is configured by the `KEI_PROXY_VERSION` GitHub Actions variable (the
workflow currently defaults it to `v0.1.0`); change that variable before a
CLI release when the proxy is upgraded. A leading `v` is allowed for readable
pinning, but release paths and filenames use the normalized unprefixed version.
The release runner fetches the proxy
from the shared bucket using its existing AWS identity, so no new credentials
or AWS role configuration are needed.

The CLI-side release flow assumes the proxy publisher provides these inputs:

```
s3://BUCKET/kei-proxy/<version>/kei-proxy_<version>_<GOOS>_<GOARCH>.tar.gz
```

For a standalone proxy installation, use the proxy publisher's standard
`kei-proxy/install.sh` endpoint. The CLI release flow fetches versioned proxy
archives directly so it can bundle the target-matched binary.

Each archive must contain an executable named `kei-proxy` and use the canonical
filename shown above. The CLI installer installs `kei` from every valid CLI archive and installs
`kei-proxy` when the optional bundled binary is present, so older or manually
built standalone kei-cli archives continue to work.

## Scope

The CLI currently supports login and logout, installation metadata, agent
assignment, installation status, and self-upgrade for the Teams, Discord, and
Slack platform identifiers. Runtime deployment workflows are intentionally
outside the CLI's scope.
