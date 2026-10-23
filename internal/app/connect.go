package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
)

// connectFlow carries the values each step of `kei connect` needs, and the
// values an earlier step produces for a later one.
type connectFlow struct {
	client          *http.Client
	store           credentialStore
	stdout          io.Writer
	stderr          io.Writer
	kind            string
	addHarness      bool
	workspaceID     string
	installationID  string
	runtimeToken    string
	configPath      string
	controlPlaneURL string
}

// connectSteps are the existing command code paths `kei connect` runs, in
// order. Each is substituted by a fake in tests so the order and idempotency
// can be asserted without touching the network, the service manager or the
// real home directory.
type connectSteps struct {
	initInstallation func(*connectFlow) error
	issueCredential  func(*connectFlow) error
	setup            func(*connectFlow) error
	bootstrap        func(*connectFlow) error
	installService   func(*connectFlow) error
	addHarness       func(*connectFlow) error
	syncHarness      func(*connectFlow) error
}

func defaultConnectSteps() connectSteps {
	return connectSteps{
		initInstallation: connectInitInstallation,
		issueCredential:  connectIssueCredential,
		setup:            connectSetup,
		bootstrap:        connectBootstrap,
		installService:   connectInstallService,
		addHarness:       connectAddHarness,
		syncHarness:      connectSyncHarness,
	}
}

// runConnectCommand sets up a harness against Kei in one command: it prints the
// organization and workspace, asks once, then runs the existing commands' code
// paths in order. A re-run resumes at the first incomplete step: when the local
// runtime configuration already holds a credential, the credential is never
// re-requested (the server shows it once).
func runConnectCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client, store credentialStore, steps connectSteps) int {
	lc := newCommandFlags("", "connect", "Set up a harness against Kei", stdout, stderr).WithArgs("HARNESS")
	workspace := lc.fs.String("workspace", "", "workspace name (default: the organization's default workspace)")
	yes := lc.fs.Bool("yes", false, "confirm without prompting")
	// Intercept -h/--help before parsePositionals (positionals before flags).
	for _, a := range args {
		if a == "-h" || a == "--help" {
			lc.printUsage(lc.stdout)
			return 0
		}
	}
	positional, err := parsePositionals(lc.fs, args, 1)
	if err != nil {
		fmt.Fprintln(stderr, "connect requires exactly one harness: claude-code|codex|opencode|pi")
		return 2
	}
	harnessName := positional[0]
	kind, addHarness, ok := connectHarnessKind(harnessName)
	if !ok {
		fmt.Fprintf(stderr, "connect: unknown harness %q; expected claude-code|codex|opencode|pi\n", harnessName)
		return 2
	}

	configPath, err := defaultConfigPath()
	if err != nil {
		fmt.Fprintf(stderr, "connect: %v\n", err)
		return 1
	}
	baseURL, err := normalizedKeiWebURL(keiWebURL())
	if err != nil {
		fmt.Fprintf(stderr, "connect: %v\n", err)
		return 2
	}
	cliToken, err := store.Load(baseURL)
	if err != nil {
		fmt.Fprintln(stderr, "connect: not logged in; run kei login first")
		return 1
	}
	orgID, err := organizationIDFromCLIToken(cliToken)
	if err != nil {
		fmt.Fprintf(stderr, "connect: %v; run kei login again\n", err)
		return 1
	}
	orgName, err := getOrganizationName(context.Background(), client, baseURL, cliToken, orgID)
	if err != nil {
		fmt.Fprintf(stderr, "connect: warning: could not fetch organization name: %v\n", err)
		orgName = orgID
	}
	workspaces, err := listWorkspaces(context.Background(), client, baseURL, cliToken)
	if err != nil {
		fmt.Fprintf(stderr, "connect: %v\n", err)
		return 1
	}
	var workspaceID, workspaceName string
	if *workspace != "" {
		ws, err := resolveWorkspaceByName(context.Background(), client, baseURL, cliToken, *workspace)
		if err != nil {
			fmt.Fprintf(stderr, "connect: %v\n", err)
			return 1
		}
		workspaceID, workspaceName = ws.ID, ws.Name
	} else {
		selected, found := pickDefaultWorkspace(workspaces)
		if !found {
			fmt.Fprintln(stderr, "connect: no workspaces found; create one with kei workspaces create")
			return 1
		}
		workspaceID, workspaceName = selected.ID, selected.Name
	}
	if workspaceName == "" {
		workspaceName = workspaceID
	}

	fmt.Fprintf(stdout, "Organization: %s\nWorkspace: %s\n", orgName, workspaceName)
	if !confirmWrite(stdin, stderr, *yes, "Connect this machine to Kei?") {
		fmt.Fprintln(stderr, "connect aborted")
		return 2
	}

	flow := &connectFlow{
		client:          client,
		store:           store,
		stdout:          stdout,
		stderr:          stderr,
		kind:            kind,
		addHarness:      addHarness,
		workspaceID:     workspaceID,
		configPath:      configPath,
		controlPlaneURL: baseURL,
	}

	if connectConfigComplete(configPath) {
		fmt.Fprintf(stdout, "Runtime credential already configured at %s; resuming.\n", configPath)
	} else {
		if err := steps.initInstallation(flow); err != nil {
			fmt.Fprintf(stderr, "connect: bot init: %v\n", err)
			return 1
		}
		if err := steps.issueCredential(flow); err != nil {
			fmt.Fprintf(stderr, "connect: credential: %v\n", err)
			return 1
		}
		if err := steps.setup(flow); err != nil {
			fmt.Fprintf(stderr, "connect: setup: %v\n", err)
			return 1
		}
	}
	if err := steps.bootstrap(flow); err != nil {
		fmt.Fprintf(stderr, "connect: runtime bootstrap: %v\n", err)
		return 1
	}
	if !connectServiceInstalled() {
		if err := steps.installService(flow); err != nil {
			fmt.Fprintf(stderr, "connect: runtime service install: %v\n", err)
			return 1
		}
	}
	if flow.addHarness {
		if err := steps.addHarness(flow); err != nil {
			fmt.Fprintf(stderr, "connect: harness add: %v\n", err)
			return 1
		}
	}
	if err := steps.syncHarness(flow); err != nil {
		fmt.Fprintf(stderr, "connect: harness sync: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Connected %s to Kei.\n", harnessName)
	return 0
}

// connectHarnessKind maps a user-facing harness name to the harness kind the
// control plane knows and whether it needs `kei harness add`. Desktop harnesses
// (Claude Code, Codex, OpenCode) are sessions of the installation and are
// configured by sync alone; pi is a custom (SDK) harness and is registered.
func connectHarnessKind(harnessName string) (kind string, addHarness bool, ok bool) {
	switch harnessName {
	case "claude-code":
		return "claude_code", false, true
	case "codex":
		return "codex", false, true
	case "opencode":
		return "opencode", false, true
	case "pi":
		return "custom", true, true
	}
	return "", false, false
}

// connectInstallationName is the installation display name kei connect creates,
// matching the console's `kind@host` convention.
func connectInstallationName(kind string) string {
	return kind + "@" + strings.TrimPrefix(hostname(), "kei@")
}

// connectConfigComplete reports whether the local runtime configuration already
// holds a runtime credential, so a re-run skips bot init, credential and setup.
func connectConfigComplete(path string) bool {
	config, err := loadRuntimeConfig(path)
	return err == nil && config.RuntimeToken != ""
}

// connectServiceInstalled reports whether the runtime service definition exists.
func connectServiceInstalled() bool {
	path := serviceDefinitionPath()
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func connectWorkspaceName(workspaces []workspaceInfo, id string) string {
	for _, w := range workspaces {
		if w.ID == id {
			return w.Name
		}
	}
	return ""
}

// --- the real steps: each calls the existing command's code path ---

func connectInitInstallation(f *connectFlow) error {
	installation, err := createBotInstallation(context.Background(), keiWebURL(), "", "cli", connectInstallationName(f.kind), f.workspaceID, io.Discard, f.client, f.store)
	if err != nil {
		return err
	}
	f.installationID = installation.ID
	return nil
}

func connectIssueCredential(f *connectFlow) error {
	baseURL, err := normalizedKeiWebURL(keiWebURL())
	if err != nil {
		return err
	}
	cliToken, err := f.store.Load(baseURL)
	if err != nil {
		return errors.New("not logged in; run kei login first")
	}
	token, status, err := requestRuntimeCredentialAction(context.Background(), f.client, baseURL, cliToken, f.installationID, f.workspaceID, "credential")
	if err != nil {
		return err
	}
	if status == http.StatusConflict {
		return errors.New("this installation already has a runtime credential; rotate it with kei bot credential --rotate --installation " + f.installationID + ", then re-run")
	}
	if status != http.StatusOK {
		return fmt.Errorf("credential request returned %d", status)
	}
	f.runtimeToken = token
	return nil
}

// connectSetup runs the existing setup code path (verify the runtime token,
// then write the 0600 config). The credential reaches setup on stdin, never on
// the command line, so it is never exposed in process arguments.
func connectSetup(f *connectFlow) error {
	args := []string{
		"--config", f.configPath,
		"--control-plane-url", f.controlPlaneURL,
		"--harness-url", "http://127.0.0.1:8088",
		"--proxy-path", defaultProxyPath(),
	}
	if code := runSetupCommand(args, f.stdout, f.stderr, strings.NewReader(f.runtimeToken+"\n"), f.client); code != 0 {
		return fmt.Errorf("kei setup exited %d", code)
	}
	return nil
}

func connectBootstrap(f *connectFlow) error {
	if code := runRuntimeBootstrapCommand([]string{"--config", f.configPath}, f.stdout, f.stderr); code != 0 {
		return fmt.Errorf("kei runtime bootstrap exited %d", code)
	}
	return nil
}

func connectInstallService(f *connectFlow) error {
	if code := runServiceInstallCommand([]string{"--config", f.configPath}, f.stdout, f.stderr, f.client, f.store); code != 0 {
		return fmt.Errorf("kei runtime service install exited %d", code)
	}
	return nil
}

func connectAddHarness(f *connectFlow) error {
	if code := runHarnessAdd([]string{"--kind", f.kind}, f.stdout, f.stderr, f.client, f.store, harness.Default); code != 0 {
		return fmt.Errorf("kei harness add --kind %s exited %d", f.kind, code)
	}
	return nil
}

func connectSyncHarness(f *connectFlow) error {
	if code := runHarnessSync([]string{"--harness", f.kind, "--config", f.configPath}, f.stdout, f.stderr, f.client, f.store, harness.Default); code != 0 {
		return fmt.Errorf("kei harness sync --harness %s exited %d", f.kind, code)
	}
	return nil
}
