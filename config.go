package main

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// runtimeConfig contains the values needed to run a local harness against a
// Kei runtime installation. The runtime token is deliberately stored in a
// user-owned file with restrictive permissions; it is never printed by the
// CLI.
type runtimeConfig struct {
	ControlPlaneURL string
	RuntimeToken    string
	HarnessURL      string
	ProxyPath       string
	ProxyRegistry   string
	ModelEndpoint   string
	Model           string
}

func defaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "kei.yaml"), nil
}

func normalizedConfigURL(raw, name string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%s must be an absolute http(s) URL", name)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%s must not contain a query or fragment", name)
	}
	return raw, nil
}

func (c runtimeConfig) validate() error {
	if c.RuntimeToken == "" {
		return errors.New("runtime token is required")
	}
	if _, err := normalizedConfigURL(c.ControlPlaneURL, "control-plane URL"); err != nil {
		return err
	}
	if _, err := normalizedConfigURL(c.HarnessURL, "harness URL"); err != nil {
		return err
	}
	return nil
}

func loadRuntimeConfig(path string) (runtimeConfig, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return runtimeConfig{}, err
	}
	var config runtimeConfig
	scanner := bufio.NewScanner(strings.NewReader(string(contents)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return runtimeConfig{}, fmt.Errorf("invalid config line %q", line)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if strings.HasPrefix(value, "\"") {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				return runtimeConfig{}, fmt.Errorf("decode config value %q: %w", key, err)
			}
			value = decoded
		}
		switch key {
		case "control_plane_url":
			config.ControlPlaneURL = value
		case "runtime_token":
			config.RuntimeToken = value
		case "harness_url":
			config.HarnessURL = value
		case "proxy_path":
			config.ProxyPath = value
		case "proxy_registry":
			config.ProxyRegistry = value
		case "model_endpoint":
			config.ModelEndpoint = value
		case "model":
			config.Model = value
		default:
			return runtimeConfig{}, fmt.Errorf("unknown config key %q", key)
		}
	}
	if err := scanner.Err(); err != nil {
		return runtimeConfig{}, fmt.Errorf("read config: %w", err)
	}
	return config, nil
}

func saveRuntimeConfig(path string, config runtimeConfig) error {
	if err := config.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	contents := strings.Join([]string{
		"# Kei CLI runtime configuration. Keep this file private.",
		"control_plane_url: " + strconv.Quote(config.ControlPlaneURL),
		"runtime_token: " + strconv.Quote(config.RuntimeToken),
		"harness_url: " + strconv.Quote(config.HarnessURL),
		"proxy_path: " + strconv.Quote(config.ProxyPath),
		"proxy_registry: " + strconv.Quote(config.ProxyRegistry),
		"model_endpoint: " + strconv.Quote(config.ModelEndpoint),
		"model: " + strconv.Quote(config.Model),
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("protect config: %w", err)
	}
	return nil
}
