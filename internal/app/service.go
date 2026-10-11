package app

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// sysCommand is the entry point for all launchctl/systemctl invocations.
// Tests override it to avoid touching the real system. The default
// implementation panics under test when KEI_SERVICE_TEST_GUARD is set,
// ensuring no test accidentally execs a real service manager.
var sysCommand = func(name string, args ...string) *exec.Cmd {
	if os.Getenv("KEI_SERVICE_TEST_GUARD") != "" {
		panic(fmt.Sprintf("service: exec %q %v prohibited without sysCommand override", name, args))
	}
	return exec.Command(name, args...)
}

// userHomeDirFn is injectable for tests. Its default is os.UserHomeDir.
var userHomeDirFn = os.UserHomeDir

func runServiceCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "service requires a subcommand (install|uninstall|status)")
		return 2
	}
	switch args[0] {
	case "install":
		return runServiceInstallCommand(args[1:], stdout, stderr, client, store)
	case "uninstall":
		return runServiceUninstallCommand(args[1:], stdout, stderr)
	case "status":
		return runServiceStatusCommand(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown service command %q\n", args[0])
		return 2
	}
}

// serviceDefinitionPath returns the canonical service definition file path
// for the current platform.
func serviceDefinitionPath() string { return serviceDefinitionPathFor(runtime.GOOS) }

// serviceDefinitionPathFor returns the service definition file path for goos,
// or "" when goos has no supported service manager.
func serviceDefinitionPathFor(goos string) string {
	home, err := userHomeDirFn()
	if err != nil {
		return ""
	}
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", "com.haikeilabs.kei-runtime.plist")
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", "kei.service")
	default:
		return ""
	}
}

// serviceName returns the canonical service name for the current platform.
func serviceName() string { return serviceNameFor(runtime.GOOS) }

// serviceNameFor returns the service name for goos.
func serviceNameFor(goos string) string {
	switch goos {
	case "darwin":
		return "com.haikeilabs.kei-runtime"
	case "linux":
		return "kei"
	default:
		return "kei"
	}
}

func runServiceInstallCommand(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	defaultPath, err := defaultConfigPath()
	if err != nil {
		fmt.Fprintf(stderr, "service install failed: %v\n", err)
		return 1
	}
	flags := flag.NewFlagSet("service install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", defaultPath, "configuration file path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "service install accepts no positional arguments")
		return 2
	}

	svcPath := serviceDefinitionPath()
	if svcPath == "" {
		fmt.Fprintln(stderr, "service install: unsupported platform")
		return 1
	}

	config, err := loadRuntimeConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "service install: read config: %v\n", err)
		return 1
	}
	if err := config.validate(); err != nil {
		fmt.Fprintf(stderr, "service install: %v\n", err)
		return 1
	}

	keiBinary, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "service install: determine kei binary path: %v\n", err)
		return 1
	}

	// Resolve symlinks so launchd/systemd gets the real path
	resolved, err := filepath.EvalSymlinks(keiBinary)
	if err == nil {
		keiBinary = resolved
	}

	switch runtime.GOOS {
	case "darwin":
		if err := installLaunchdService(svcPath, keiBinary, *configPath, stdout); err != nil {
			fmt.Fprintf(stderr, "service install failed: %v\n", err)
			return 1
		}
	case "linux":
		if err := installSystemdService(svcPath, keiBinary, *configPath, stdout); err != nil {
			fmt.Fprintf(stderr, "service install failed: %v\n", err)
			return 1
		}
	default:
		fmt.Fprintln(stderr, "service install: unsupported platform")
		return 1
	}
	return 0
}

func execUserID() string {
	return strconv.Itoa(os.Geteuid())
}

func isLaunchdServiceLoaded(name string) bool {
	cmd := sysCommand("launchctl", "print", "gui/"+execUserID()+"/"+name)
	return cmd.Run() == nil
}

// launchdLogPath is where the LaunchAgent writes the runtime's stdout and
// stderr.
func launchdLogPath(home string) string {
	return filepath.Join(home, "Library", "Logs", "kei-runtime.log")
}

func installLaunchdService(path, keiBinary, configPath string, stdout io.Writer) error {
	name := serviceName()
	home, err := userHomeDirFn()
	if err != nil {
		return fmt.Errorf("determine home directory: %w", err)
	}
	logFile := launchdLogPath(home)
	logDir := filepath.Dir(logFile)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("create Logs directory: %w", err)
	}

	// If already loaded, bootout first so the new plist takes effect
	if isLaunchdServiceLoaded(name) {
		fmt.Fprintf(stdout, "Service %s is already loaded; reloading.\n", name)
		bootout := sysCommand("launchctl", "bootout", "gui/"+execUserID()+"/"+name)
		bootout.Stdout = stdout
		bootout.Stderr = io.Discard
		if err := bootout.Run(); err != nil {
			return fmt.Errorf("bootout existing service: %w", err)
		}
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>runtime</string>
		<string>heartbeat</string>
		<string>--config</string>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/usr/local/bin:/usr/bin:/bin</string>
	</dict>
</dict>
</plist>
`, name, keiBinary, configPath, logFile, logFile)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	// Load the service with launchctl
	cmd := sysCommand("launchctl", "load", path)
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		// If load fails, try bootstrap (macOS newer versions)
		cmd2 := sysCommand("launchctl", "bootstrap", "gui/"+execUserID(), path)
		cmd2.Stdout = stdout
		cmd2.Stderr = io.Discard
		if err2 := cmd2.Run(); err2 != nil {
			return fmt.Errorf("load service (tried launchctl load and bootstrap): %v / %v", err, err2)
		}
	}

	fmt.Fprintf(stdout, "Installed and loaded Kei runtime service (%s).\n", name)
	return nil
}

func installSystemdService(path, keiBinary, configPath string, stdout io.Writer) error {
	unit := fmt.Sprintf(`[Unit]
Description=Kei Runtime Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s runtime heartbeat --config %s
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, keiBinary, configPath)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create systemd user directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}

	cmd := sysCommand("systemctl", "--user", "daemon-reload")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	cmd2 := sysCommand("systemctl", "--user", "enable", serviceName())
	if err := cmd2.Run(); err != nil {
		return fmt.Errorf("systemctl enable: %w", err)
	}
	cmd3 := sysCommand("systemctl", "--user", "start", serviceName())
	if err := cmd3.Run(); err != nil {
		return fmt.Errorf("systemctl start: %w", err)
	}

	fmt.Fprintf(stdout, "Installed and started Kei runtime service (%s).\n", serviceName())
	return nil
}

func runServiceUninstallCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("service uninstall", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "service uninstall accepts no positional arguments")
		return 2
	}

	svcPath := serviceDefinitionPath()
	if svcPath == "" {
		fmt.Fprintln(stderr, "service uninstall: unsupported platform")
		return 1
	}

	switch runtime.GOOS {
	case "darwin":
		if err := uninstallLaunchdService(svcPath, stdout); err != nil {
			fmt.Fprintf(stderr, "service uninstall failed: %v\n", err)
			return 1
		}
	case "linux":
		if err := uninstallSystemdService(stdout); err != nil {
			fmt.Fprintf(stderr, "service uninstall failed: %v\n", err)
			return 1
		}
	default:
		fmt.Fprintln(stderr, "service uninstall: unsupported platform")
		return 1
	}

	// Remove the service definition file
	if err := os.Remove(svcPath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "service uninstall: remove definition: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Uninstalled Kei runtime service (%s).\n", serviceName())
	return 0
}

func uninstallLaunchdService(path string, stdout io.Writer) error {
	name := serviceName()
	// Unload with launchctl
	cmd := sysCommand("launchctl", "unload", path)
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		// Try bootout on newer macOS
		cmd2 := sysCommand("launchctl", "bootout", "gui/"+execUserID()+"/"+name)
		cmd2.Stdout = stdout
		cmd2.Stderr = io.Discard
		if err2 := cmd2.Run(); err2 != nil {
			return fmt.Errorf("unload service (tried launchctl unload and bootout): %v / %v", err, err2)
		}
	}
	return nil
}

func uninstallSystemdService(stdout io.Writer) error {
	cmd := sysCommand("systemctl", "--user", "stop", serviceName())
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	_ = cmd.Run() // Best effort stop

	cmd2 := sysCommand("systemctl", "--user", "disable", serviceName())
	cmd2.Stdout = stdout
	cmd2.Stderr = io.Discard
	_ = cmd2.Run() // Best effort disable

	return nil
}

func runServiceStatusCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("service status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "service status accepts no positional arguments")
		return 2
	}

	svcPath := serviceDefinitionPath()
	if svcPath == "" {
		fmt.Fprintln(stderr, "service status: unsupported platform")
		return 1
	}

	if _, err := os.Stat(svcPath); os.IsNotExist(err) {
		fmt.Fprintln(stdout, "Kei runtime service is not installed.")
		return 0
	}

	switch runtime.GOOS {
	case "darwin":
		return statusLaunchdService(stdout, stderr)
	case "linux":
		return statusSystemdService(stdout, stderr)
	default:
		fmt.Fprintln(stderr, "service status: unsupported platform")
		return 1
	}
}

func statusLaunchdService(stdout, stderr io.Writer) int {
	name := serviceName()
	cmd := sysCommand("launchctl", "print", "gui/"+execUserID()+"/"+name)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		// Fallback to older launchctl
		out, _ := sysCommand("launchctl", "list", name).Output()
		if strings.Contains(string(out), name) {
			fmt.Fprintf(stdout, "Service %s is loaded.\n", name)
			return 0
		}
		fmt.Fprintf(stdout, "Service %s is installed but not running.\n", name)
		return 0
	}
	return 0
}

func statusSystemdService(stdout, stderr io.Writer) int {
	cmd := sysCommand("systemctl", "--user", "status", serviceName())
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		// systemctl status returns non-zero when inactive; that's expected
		fmt.Fprintf(stdout, "Service %s is installed but not running.\n", serviceName())
		return 0
	}
	return 0
}
