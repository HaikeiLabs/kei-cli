package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

type runtimeIdentity struct {
	ID            string `json:"id"`
	OrgID         string `json:"org_id"`
	Platform      string `json:"platform"`
	Status        string `json:"status"`
	BindingStatus string `json:"binding_status"`
}

func runSetupCommand(args []string, stdout, stderr io.Writer, stdin io.Reader, client *http.Client) int {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath, err := defaultConfigPath()
	if err != nil {
		fmt.Fprintf(stderr, "setup failed: %v\n", err)
		return 1
	}
	path := flags.String("config", configPath, "configuration file path")
	controlPlaneURL := flags.String("control-plane-url", defaultKeiWebURL, "Kei runtime control-plane URL")
	runtimeToken := flags.String("runtime-token", "", "runtime installation token (prefer the interactive prompt)")
	harnessURL := flags.String("harness-url", "http://127.0.0.1:8088", "local headless harness URL")
	proxyPath := flags.String("proxy-path", defaultProxyPath(), "kei-proxy executable path")
	proxyRegistry := flags.String("proxy-registry", "", "kei-proxy tool registry path")
	modelEndpoint := flags.String("model-endpoint", "", "optional OpenAI-compatible model endpoint")
	model := flags.String("model", "", "optional model name")
	skipVerify := flags.Bool("skip-verify", false, "do not verify the runtime token against Kei")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "setup accepts no positional arguments")
		return 2
	}
	provided := make(map[string]bool)
	flags.Visit(func(flag *flag.Flag) { provided[flag.Name] = true })

	reader := bufio.NewReader(stdin)
	if !provided["control-plane-url"] {
		*controlPlaneURL = promptLine(reader, stdout, "Kei control-plane URL", *controlPlaneURL)
	}
	if *runtimeToken == "" {
		var promptErr error
		*runtimeToken, promptErr = promptSecret(reader, stdin, stdout, "Runtime installation token: ")
		if promptErr != nil {
			fmt.Fprintf(stderr, "setup failed: %v\n", promptErr)
			return 1
		}
	}
	if !provided["harness-url"] {
		*harnessURL = promptLine(reader, stdout, "Local harness URL", *harnessURL)
	}
	if !provided["proxy-path"] && *proxyPath == "" {
		*proxyPath = promptLine(reader, stdout, "kei-proxy path", "kei-proxy")
	}

	config := runtimeConfig{
		ControlPlaneURL: strings.TrimSpace(*controlPlaneURL),
		RuntimeToken:    strings.TrimSpace(*runtimeToken),
		HarnessURL:      strings.TrimSpace(*harnessURL),
		ProxyPath:       strings.TrimSpace(*proxyPath),
		ProxyRegistry:   strings.TrimSpace(*proxyRegistry),
		ModelEndpoint:   strings.TrimSpace(*modelEndpoint),
		Model:           strings.TrimSpace(*model),
	}
	if err := config.validate(); err != nil {
		fmt.Fprintf(stderr, "setup failed: %v\n", err)
		return 1
	}
	if !*skipVerify {
		identity, err := verifyRuntimeToken(context.Background(), client, config.ControlPlaneURL, config.RuntimeToken)
		if err != nil {
			fmt.Fprintf(stderr, "setup failed: could not verify runtime token: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Verified runtime installation %s for organization %s (%s).\n", identity.ID, identity.OrgID, identity.Platform)
	}
	if err := saveRuntimeConfig(*path, config); err != nil {
		fmt.Fprintf(stderr, "setup failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Saved Kei CLI configuration to %s (permissions 0600).\n", *path)
	return 0
}

func promptLine(reader *bufio.Reader, stdout io.Writer, label, defaultValue string) string {
	if defaultValue == "" {
		fmt.Fprintf(stdout, "%s: ", label)
	} else {
		fmt.Fprintf(stdout, "%s [%s]: ", label, defaultValue)
	}
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return defaultValue
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue
	}
	return line
}

func promptSecret(reader *bufio.Reader, stdin io.Reader, stdout io.Writer, label string) (string, error) {
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprint(stdout, label)
		secret, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(stdout)
		return strings.TrimSpace(string(secret)), err
	}
	return promptLine(reader, stdout, strings.TrimSuffix(label, " "), ""), nil
}

func defaultProxyPath() string {
	if path, err := exec.LookPath("kei-proxy"); err == nil {
		return path
	}
	if _, err := os.Stat("/tmp/kei-proxy"); err == nil {
		return "/tmp/kei-proxy"
	}
	return "kei-proxy"
}

func verifyRuntimeToken(ctx context.Context, client *http.Client, controlPlaneURL, token string) (*runtimeIdentity, error) {
	baseURL, err := normalizedConfigURL(controlPlaneURL, "control-plane URL")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/runtime/whoami", nil)
	if err != nil {
		return nil, fmt.Errorf("build verification request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("runtime identity request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("runtime identity request returned HTTP %d", response.StatusCode)
	}
	var identity runtimeIdentity
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&identity); err != nil {
		return nil, fmt.Errorf("decode runtime identity response: %w", err)
	}
	if identity.ID == "" || identity.OrgID == "" || identity.Platform == "" {
		return nil, errors.New("runtime identity response is incomplete")
	}
	return &identity, nil
}
