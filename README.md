# Kei CLI

The Kei CLI manages Kei bot installation metadata. It authenticates operators,
creates an installation for a bot platform, assigns agents, and reports the
installation status. Runtime deployment and credentials are managed outside
this CLI.

## Install

The recommended installation is directly from the public GitHub module:

```sh
go install github.com/HaikeiLabs/kei-cli@latest
```

This installs the executable as `kei-cli` in Go's binary directory. Ensure that
directory is on your `PATH`:

```sh
export PATH="$(go env GOPATH)/bin:$PATH"
```

Add the same line to `~/.zshrc` (or your shell's startup file) to make it
permanent, then verify the installation:

```sh
kei-cli help
```

If you prefer the shorter command name, add `alias kei=kei-cli` to your shell.

To build from source instead:

```sh
go build -o tmp/kei-cli .
./tmp/kei-cli help
```

## Login

The CLI uses Kei device authorization. It prints a browser URL and one-time
verification code; complete the flow in your browser:

```sh
kei-cli login --api-url https://app.haikeilabs.com
```

The login token is stored in the operating system credential store. It is not
printed or written to the repository.

## Manage an installation

Create pending installation metadata for a bot:

```sh
kei-cli bot init \
  --platform teams \
  --name "Customer Teams"
```

The command prints the installation ID. Use that ID to inspect the installation
or manage its agent assignments:

```sh
kei-cli bot status --installation INSTALLATION_ID
kei-cli bot agents list --installation INSTALLATION_ID
kei-cli bot agents add --installation INSTALLATION_ID --agent AGENT_ID
kei-cli bot agents add --installation INSTALLATION_ID --agent AGENT_ID --default
kei-cli bot agents remove --installation INSTALLATION_ID --agent AGENT_ID
```

All commands accept `--api-url URL` when using a Kei environment other than
the default public service.

## Scope

The CLI currently supports login, installation metadata, agent assignment, and
installation status for the Teams, Discord, and Slack platform identifiers.
Runtime deployment workflows are intentionally outside the CLI's scope.
