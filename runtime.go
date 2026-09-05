package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
)

func runRuntimeCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "runtime requires the bootstrap subcommand")
		return 2
	}
	if args[0] != "bootstrap" {
		fmt.Fprintf(stderr, "unknown runtime command %q\n", args[0])
		return 2
	}
	return runRuntimeBootstrapCommand(args[1:], stdout, stderr)
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
		fmt.Fprintf(stderr, "runtime bootstrap failed: read config: %v\n", err)
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
