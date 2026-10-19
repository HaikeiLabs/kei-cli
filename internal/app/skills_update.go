package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// npxSkillsUpdateArgs is the skills CLI invocation that refreshes the stale
// Haikei skills in the global install.
func npxSkillsUpdateArgs(stale []string) []string {
	args := []string{"-y", "skills", "update", "-g"}
	args = append(args, stale...)
	return append(args, "-y")
}

// runSkillsUpdate is `kei skills update`: it refreshes the stale Haikei skills
// by shelling out to the skills CLI. There is no installer here — when npx is
// not on PATH it prints the command to run instead.
func runSkillsUpdate(stdout, stderr io.Writer, client *http.Client) int {
	cachePath, err := defaultSkillsUpstreamCachePath()
	if err != nil {
		fmt.Fprintf(stderr, "skills update: %v\n", err)
		return 1
	}
	stale, total, err := staleHaikeiSkills(context.Background(), client, cachePath, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "skills update: %v\n", err)
		return 1
	}
	if total == 0 {
		fmt.Fprintln(stdout, "No Haikei skills are installed.")
		return 0
	}
	if len(stale) == 0 {
		fmt.Fprintf(stdout, "All %d Haikei skills are up to date.\n", total)
		return 0
	}
	args := npxSkillsUpdateArgs(stale)
	npxPath, err := exec.LookPath("npx")
	if err != nil {
		fmt.Fprintf(stdout, "npx is not on PATH; run: npx %s\n", strings.Join(args, " "))
		return 0
	}
	cmd := runCommand(context.Background(), npxPath, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			fmt.Fprintf(stderr, "skills update: %v\n", err)
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "skills update: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Updated %d Haikei skills.\n", len(stale))
	return 0
}
