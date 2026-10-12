package app

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
	"github.com/HaikeiLabs/kei-connector-contracts/providers"
)

// proxySkillSource is one skills_repo connector read through `kei-proxy
// connector invoke` (HAI-431): kei-proxy decides and audits every call and
// holds the repository credential, so the CLI never calls the repository
// host or reads a credential. The runtime token reaches kei-proxy only
// through its environment.
type proxySkillSource struct {
	proxyPath   string
	config      runtimeConfig
	connectorID string
}

func (s proxySkillSource) ListSkills(ctx context.Context) (providers.SkillsRepoListing, error) {
	var listing providers.SkillsRepoListing
	err := s.invoke(ctx, providers.SkillsListPayload{}.Capability(), "skills", &listing)
	return listing, err
}

func (s proxySkillSource) FetchSkill(ctx context.Context, name string) (providers.SkillsRepoArchive, error) {
	var archive providers.SkillsRepoArchive
	if err := providers.ValidateSkillName(name); err != nil {
		return archive, err
	}
	err := s.invoke(ctx, providers.SkillsFetchPayload{}.Capability(), "skills/"+name, &archive)
	return archive, err
}

// invoke runs one governed read and decodes its result. Anything but an
// executed allow (a non-zero exit, a deny, no result) is an error.
func (s proxySkillSource) invoke(ctx context.Context, capability, resource string, result any) error {
	var stdout, stderr bytes.Buffer
	cmd := runCommand(ctx, s.proxyPath, "connector", "invoke", "--connector", s.connectorID, "--capability", capability, "--action", "read", "--resource", resource)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(),
		"KEI_RUNTIME_CONTROL_PLANE_URL="+s.config.ControlPlaneURL,
		"KEI_RUNTIME_TOKEN="+s.config.RuntimeToken,
	)
	runErr := cmd.Run()
	var out struct {
		Decision   string          `json:"decision"`
		ReasonCode string          `json:"reason_code"`
		Reason     string          `json:"reason"`
		Result     json.RawMessage `json:"result"`
	}
	decodeErr := json.Unmarshal(stdout.Bytes(), &out)
	if decodeErr == nil && out.Decision != "" && out.Decision != "allow" {
		return fmt.Errorf("kei-proxy denied %s %s: %s", capability, resource, joinReason(out.ReasonCode, out.Reason))
	}
	if runErr != nil {
		return fmt.Errorf("kei-proxy %s %s: %w", capability, resource, runErr)
	}
	if decodeErr != nil || out.Decision != "allow" || len(out.Result) == 0 {
		return fmt.Errorf("kei-proxy %s %s: no allowed result", capability, resource)
	}
	if err := json.Unmarshal(out.Result, result); err != nil {
		return fmt.Errorf("kei-proxy %s %s: decode result: %w", capability, resource, err)
	}
	return nil
}

func joinReason(code, reason string) string {
	switch {
	case code != "" && reason != "":
		return code + " (" + reason + ")"
	case code != "":
		return code
	case reason != "":
		return reason
	}
	return "no reason given"
}

// installOrgSkills installs the bundle's permitted org skills into the
// harnesses' skill dirs. With quiet set (from harness sync), a bundle with
// no skills_repo connector prints nothing.
func installOrgSkills(ctx context.Context, config runtimeConfig, bundle harness.Bundle, harnesses []harness.Harness, dryRun, quiet bool, stdout, stderr io.Writer, env harness.Env, now time.Time) error {
	connectors, err := harness.SkillsRepoConnectors(bundle)
	if err != nil {
		return err
	}
	if len(connectors) == 0 {
		if !quiet {
			fmt.Fprintln(stdout, "No skills_repo connector is bound to this workspace; no skills to install.")
		}
		return nil
	}
	proxyPath := config.ProxyPath
	if proxyPath == "" {
		proxyPath = defaultProxyPath()
	}
	sources := make([]harness.SkillSource, len(connectors))
	for i, id := range connectors {
		sources[i] = proxySkillSource{proxyPath: proxyPath, config: config, connectorID: id}
	}
	result, err := harness.InstallSkills(ctx, sources, bundle, harnesses, harness.SkillsOpts{Env: env, DryRun: dryRun, Out: stdout, Now: now})
	printLines(stderr, result.Warnings)
	return err
}

func runSkillsCommand(args []string, stdout, stderr io.Writer, client *http.Client) int {
	if len(args) == 0 || args[0] != "install" {
		fmt.Fprintln(stderr, "usage: kei skills install [--harness KIND] [--dry-run] [--config PATH]")
		return 2
	}
	return runSkillsInstall(args[1:], stdout, stderr, client, harness.Default, harness.OSEnv())
}

// runSkillsInstall is `kei skills install`: the skills step of harness sync
// on its own, for the detected harnesses or one --harness KIND.
func runSkillsInstall(args []string, stdout, stderr io.Writer, client *http.Client, registry *harness.Registry, env harness.Env) int {
	flags := flag.NewFlagSet("skills install", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "runtime configuration path (default ~/.config/kei.yaml)")
	kindFilter := flags.String("harness", "", "only install into this harness kind")
	dryRun := flags.Bool("dry-run", false, "print the per-harness plan without writing")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if h, ok := registry.Get(*kindFilter); *kindFilter != "" && (!ok || len(h.SkillDirs(env)) == 0) {
		fmt.Fprintf(stderr, "--harness must be a harness kind with a skill dir: %s\n", harness.JoinOr(skillKinds(registry, env)))
		return 2
	}
	if *configPath == "" {
		path, err := defaultConfigPath()
		if err != nil {
			fmt.Fprintf(stderr, "skills install: %v\n", err)
			return 1
		}
		*configPath = path
	}
	config, err := loadRuntimeConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "skills install: read config: %v\n", err)
		return 1
	}
	if err := config.validate(); err != nil {
		fmt.Fprintf(stderr, "skills install: %v\n", err)
		return 1
	}
	ctx := context.Background()
	bundle, _, err := fetchHarnessBundle(ctx, config, client)
	if err != nil {
		fmt.Fprintf(stderr, "skills install: %v\n", err)
		return 1
	}
	if !bundle.NotAfter.After(time.Now()) {
		fmt.Fprintln(stderr, "skills install: the policy bundle has expired; run kei harness sync after renewing the runtime bundle")
		return 1
	}
	harnesses, err := selectHarnesses(registry, env, *kindFilter)
	if err != nil {
		fmt.Fprintf(stderr, "skills install: %v\n", err)
		return 1
	}
	if len(harnesses) == 0 {
		fmt.Fprintln(stdout, "No harness kinds detected locally; use --harness KIND to install into a specific one.")
		return 0
	}
	if err := installOrgSkills(ctx, config, bundle, harnesses, *dryRun, false, stdout, stderr, env, time.Now()); err != nil {
		fmt.Fprintf(stderr, "skills install: %v\n", err)
		return 1
	}
	return 0
}

// skillKinds are the registered kinds that have a skill dir.
func skillKinds(registry *harness.Registry, env harness.Env) []string {
	var kinds []string
	for _, h := range registry.All() {
		if len(h.SkillDirs(env)) > 0 {
			kinds = append(kinds, h.Kind())
		}
	}
	return kinds
}
