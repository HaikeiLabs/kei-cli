package app

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const cliUpgradeHint = "Hint: run kei upgrade to install the latest CLI."

func upgradeHintForVersions(current, latest string, requestErr error) string {
	if requestErr == nil {
		return ""
	}
	if apiCompatibilityFailure(requestErr) {
		return cliUpgradeHint
	}
	if releaseVersionPattern.MatchString(current) && releaseVersionPattern.MatchString(latest) && compareVersions(current, latest) < 0 {
		return cliUpgradeHint
	}
	return ""
}

func apiCompatibilityFailure(err error) bool {
	message := strings.ToLower(err.Error())
	for _, phrase := range []string{
		"unknown endpoint", "endpoint not found", "endpoint is not available",
		"unknown field", "unsupported field", "unrecognized field",
		"not implemented", "does not support this endpoint", "no route", "api is not available yet",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

func withCLIUpgradeHint(client *http.Client, err error) error {
	if err == nil {
		return nil
	}
	current := cliVersionFromClient(client)
	if !releaseVersionPattern.MatchString(current) {
		if hint := upgradeHintForVersions(current, "", err); hint != "" {
			return fmt.Errorf("%w; %s", err, hint)
		}
		return err
	}
	latestClient := &http.Client{Timeout: 2 * time.Second}
	latest, fetchErr := resolveReleaseVersion(latestClient, defaultReleaseBase, "kei-cli")
	if fetchErr == nil {
		if hint := upgradeHintForVersions(current, latest, err); hint != "" {
			return fmt.Errorf("%w; %s", err, hint)
		}
	}
	return err
}

func cliVersionFromClient(client *http.Client) string {
	if client == nil {
		return ""
	}
	transport, ok := client.Transport.(*clientHeaderTransport)
	if !ok {
		return ""
	}
	return clientVersion(transport.version)
}
