package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/HaikeiLabs/kei-cli/internal/harness"
)

// HAI-723 / ADR-039: the runtime no longer routes or invokes provider calls.
// Skills are read directly from the repository by the harness, not through
// kei-proxy connector invoke. The CLI renders the config; the harness
// authenticates with the repository using the credential the runtime passes
// through. The bundle must carry the connector's repo_url for the CLI to
// render a skills source; until the catalog serves it, skills install is a
// no-op with a warning.

// installOrgSkills installs the bundle's permitted org skills into the
// harnesses' skill dirs. With quiet set (from harness sync), a bundle with
// no skills_repo connector prints nothing.
//
// HAI-723 / ADR-039: the runtime no longer routes provider calls, so the CLI
// no longer reads skills through kei-proxy connector invoke. The bundle must
// carry each skills_repo connector's repo_url for the CLI to render a source;
// until the catalog serves it, skills install prints a warning and skips.
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
	// HAI-723 / ADR-039: render the skills source from the connector's
	// repo_url so the harness reads skills directly from the repository.
	if repo := skillsRepoURL(bundle); repo != "" {
		fmt.Fprintf(stdout, "skills install: rendering skills source from %s (the harness reads skills directly from the repository)\n", repo)
		return nil
	}
	fmt.Fprintln(stderr, "skills install: the bundle does not carry the skills_repo connector's repo_url; skills are not installed (HAI-723). The harness reads skills directly from the repository.")
	return nil
}

// skillsRepoURL returns the repo_url of the bundle's skills_repo connector,
// or "" when the catalog has not served one yet.
func skillsRepoURL(bundle harness.Bundle) string {
	for _, c := range bundle.Connectors {
		if c.Kind == "skills_repo" && c.RepoURL != "" {
			return c.RepoURL
		}
	}
	return ""
}

// skillsUsage is the one dispatch line for the `kei skills` subcommands.
const skillsUsage = "usage: kei skills status | kei skills update | kei skills add | kei skills install [--harness KIND] [--dry-run] [--config PATH]"

var skillsGroup = commandGroup{
	name: "skills",
	commands: []groupCommand{
		{name: "status", summary: "Show skill installation status"},
		{name: "update", summary: "Update installed skills"},
		{name: "add", summary: "Add a skill"},
		{name: "install", summary: "Install a skill"},
	},
}

func runSkillsCommand(args []string, stdout, stderr io.Writer, client *http.Client) int {
	sub, rest, code, handled := skillsGroup.dispatch(args, stdout, stderr)
	if handled {
		return code
	}
	switch sub {
	case "status":
		if len(rest) != 0 {
			fmt.Fprintln(stderr, "usage: kei skills status")
			return 2
		}
		return runSkillsStatus(stdout, stderr, client)
	case "update":
		if len(rest) != 0 {
			fmt.Fprintln(stderr, "usage: kei skills update")
			return 2
		}
		return runSkillsUpdate(stdout, stderr, client)
	case "add":
		if len(rest) != 0 {
			fmt.Fprintln(stderr, "usage: kei skills add")
			return 2
		}
		return runSkillsAdd(stdout, stderr)
	case "install":
		return runSkillsInstall(rest, stdout, stderr, client, harness.Default, harness.OSEnv())
	default:
		panic("unexpected subcommand: " + sub)
	}
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
