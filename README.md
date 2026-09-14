# Kei CLI

The Kei CLI manages Kei bot installation metadata. It authenticates operators,
creates an installation for a bot platform, assigns agents, and reports the
installation status. Runtime deployment and credentials are managed outside
this CLI.

## Install

The recommended installation is directly from the public GitHub module:

```sh
go install github.com/HaikeiLabs/kei-cli@latest
mv "$(go env GOPATH)/bin/kei-cli" "$(go env GOPATH)/bin/kei"
```

This installs the executable as `kei` in Go's binary directory. Ensure that
directory is on your `PATH`:

```sh
export PATH="$(go env GOPATH)/bin:$PATH"
```

Add the same line to `~/.zshrc` (or your shell's startup file) to make it
permanent, then verify the installation:

```sh
kei help
```

To build from source instead:

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

## Scope

The CLI currently supports login, installation metadata, agent assignment, and
installation status for the Teams, Discord, and Slack platform identifiers.
Runtime deployment workflows are intentionally outside the CLI's scope.
