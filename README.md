# Kei CLI

The Kei CLI manages Kei bot installation metadata. It authenticates operators,
creates an installation for a bot platform, assigns agents, and reports the
installation status. Runtime deployment and credentials are managed outside
this CLI.

## Install

Binary releases of kei-cli are available for macOS and Linux (arm64 and amd64).
Choose one of the following methods.

### Option 1: curl install from S3 (recommended)

Requires a configured AWS S3 release endpoint. The install script detects your
OS and architecture, downloads the matching archive, verifies its SHA-256
checksum, and places the `kei` binary in `/usr/local/bin`:

```sh
# Set the release URL base before running (required):
export AWS_S3_RELEASES_URL_BASE="https://kei-releases.s3.us-east-1.amazonaws.com"

# Install the latest version:
curl -fsSL "$AWS_S3_RELEASES_URL_BASE/kei-cli/install.sh" | bash

# Install a specific version:
curl -fsSL "$AWS_S3_RELEASES_URL_BASE/kei-cli/install.sh" | bash -s -- -v v0.1.0

# Install to a custom directory:
curl -fsSL "$AWS_S3_RELEASES_URL_BASE/kei-cli/install.sh" | bash -s -- -d ~/.local/bin
```

After installing, verify:

```sh
kei help
kei --version
```

### Option 2: go install

Requires Go 1.26+:

```sh
go install github.com/HaikeiLabs/kei-cli@latest
mv "$(go env GOPATH)/bin/kei-cli" "$(go env GOPATH)/bin/kei"
export PATH="$(go env GOPATH)/bin:$PATH"
```

Add the export line to `~/.zshrc` (or your shell's startup file) to make it
permanent.

### Upgrading

After installing, the built-in upgrade command fetches the latest published
module:

```sh
kei upgrade
```

Use `--version VERSION` to pin a specific release.

### Building from source

```sh
go build -o tmp/kei .
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
kei login --api-url https://app.haikeilabs.com
```

The approval page opens automatically. Use `--no-browser` in a headless
environment.

The login token is stored in the operating system credential store. It is not
printed or written to the repository.

## Logout

Remove the stored CLI token for a Kei environment:

```sh
kei logout --api-url https://app.haikeilabs.com
```

Logout only removes the local credential from the OS keychain; it does not
revoke the token server-side. It is idempotent: running it while not logged
in succeeds and reports that no session was stored.

## Manage an installation

Create pending installation metadata for a bot:

```sh
kei bot init \
  --platform teams \
  --name "Customer Teams"
```

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
kei bot credential --installation INSTALLATION_ID | secret-manager import
kei bot credential --installation INSTALLATION_ID --rotate | secret-manager import
```

All commands accept `--api-url URL` when using a Kei environment other than
the default public service.

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

## Scope

The CLI currently supports login and logout, installation metadata, agent
assignment, installation status, and self-upgrade for the Teams, Discord, and
Slack platform identifiers. Runtime deployment workflows are intentionally
outside the CLI's scope.
