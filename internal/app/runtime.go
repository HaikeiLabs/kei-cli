package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
)

var runCommand = exec.CommandContext

func runRuntimeCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "runtime requires a subcommand (bootstrap|heartbeat|service)")
		return 2
	}
	if args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(stdout, "Usage: kei runtime <command>\n\nCommands:\n  bootstrap  Bootstrap the runtime\n  heartbeat  Send a runtime heartbeat\n  service    Manage the runtime service")
		return 0
	}
	switch args[0] {
	case "bootstrap":
		return runRuntimeBootstrapCommand(args[1:], stdout, stderr)
	case "heartbeat":
		return runRuntimeHeartbeatCommand(args[1:], stdout, stderr)
	case "service":
		return runServiceCommand(args[1:], stdout, stderr, client, store)
	default:
		fmt.Fprintf(stderr, "unknown runtime command %q\n", args[0])
		return 2
	}
}

func runRuntimeBootstrapCommand(args []string, stdout, stderr io.Writer) int {
	defaultPath, err := defaultConfigPath()
	if err != nil {
		fmt.Fprintf(stderr, "runtime bootstrap failed: %v\n", err)
		return 1
	}
	flags := flag.NewFlagSet("runtime bootstrap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultPath, "configuration file path")
	proxyOverride := flags.String("proxy-path", "", "override the configured kei-proxy path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runtime bootstrap accepts no positional arguments")
		return 2
	}

	config, err := loadRuntimeConfig(*configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "runtime bootstrap failed: config file not found at %s\nRun 'kei setup' to create it.\n", *configPath)
		} else {
			fmt.Fprintf(stderr, "runtime bootstrap failed: read config %s: %v\n", *configPath, err)
		}
		return 1
	}
	if err := config.validate(); err != nil {
		fmt.Fprintf(stderr, "runtime bootstrap failed: %v\n", err)
		return 1
	}
	proxyPath := config.ProxyPath
	if *proxyOverride != "" {
		proxyPath = *proxyOverride
	}
	if proxyPath == "" {
		proxyPath = defaultProxyPath()
	}

	ctx := context.Background()
	command := exec.CommandContext(ctx, proxyPath, "runtime", "bootstrap")
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = append(os.Environ(),
		"KEI_RUNTIME_CONTROL_PLANE_URL="+config.ControlPlaneURL,
		"KEI_RUNTIME_TOKEN="+config.RuntimeToken,
	)
	if err := command.Run(); err != nil {
		fmt.Fprintf(stderr, "runtime bootstrap failed: %v\n", err)
		return 1
	}
	return 0
}

func runRuntimeHeartbeatCommand(args []string, stdout, stderr io.Writer) int {
	defaultPath, err := defaultConfigPath()
	if err != nil {
		fmt.Fprintf(stderr, "runtime heartbeat: %v\n", err)
		return 1
	}
	flags := flag.NewFlagSet("runtime heartbeat", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultPath, "configuration file path")
	proxyOverride := flags.String("proxy-path", "", "override the configured kei-proxy path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "runtime heartbeat accepts no positional arguments")
		return 2
	}
	config, err := loadRuntimeConfig(*configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "runtime heartbeat failed: config file not found at %s\nRun 'kei setup' to create it.\n", *configPath)
		} else {
			fmt.Fprintf(stderr, "runtime heartbeat failed: read config %s: %v\n", *configPath, err)
		}
		return 1
	}
	if err := config.validate(); err != nil {
		fmt.Fprintf(stderr, "runtime heartbeat: %v\n", err)
		return 1
	}
	proxyPath := config.ProxyPath
	if *proxyOverride != "" {
		proxyPath = *proxyOverride
	}
	if proxyPath == "" {
		proxyPath = defaultProxyPath()
	}
	ctx := context.Background()
	command := runCommand(ctx, proxyPath, "runtime", "heartbeat")
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = append(os.Environ(),
		"KEI_RUNTIME_CONTROL_PLANE_URL="+config.ControlPlaneURL,
		"KEI_RUNTIME_TOKEN="+config.RuntimeToken,
	)
	if err := command.Run(); err != nil {
		fmt.Fprintf(stderr, "runtime heartbeat: %v\n", err)
		return 1
	}
	return 0
}
