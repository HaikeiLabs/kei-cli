package harness

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"
)

// RuntimeInstallation is one runtime installation as the control plane's
// runtime-installations list reports it.
type RuntimeInstallation struct {
	ID              string
	Platform        string
	DisplayName     string
	Status          string
	LastHeartbeatAt *time.Time
}

// RuntimeHealth is the health one runtime-installation get reports. Nil
// fields were not reported.
type RuntimeHealth struct {
	RuntimeVersion  *string
	LastHeartbeatAt *time.Time
	PolicyRevision  *int64
	BundleState     *string
}

// InstallationSource reads runtime installations from the control plane
// through calls the CLI already makes (no new routes).
type InstallationSource interface {
	ListInstallations(ctx context.Context) ([]RuntimeInstallation, error)
	InstallationHealth(ctx context.Context, id string) (RuntimeHealth, error)
}

// OpenWebUIInstallation is a platform=openwebui runtime installation as
// `kei harness list` shows it. Nil means the control plane did not report it.
type OpenWebUIInstallation struct {
	ID              string     `json:"id"`
	DisplayName     string     `json:"display_name"`
	Status          string     `json:"status"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	KeiProxyVersion *string    `json:"kei_proxy_version"`
	BundleRevision  *int64     `json:"bundle_revision"`
	BundleState     *string    `json:"bundle_state"`
}

// ListOpenWebUIInstallations returns the Open WebUI installations with their
// health. A failed health read leaves that installation's health unset
// rather than failing the list.
func ListOpenWebUIInstallations(ctx context.Context, src InstallationSource) ([]OpenWebUIInstallation, error) {
	items, err := src.ListInstallations(ctx)
	if err != nil {
		return nil, err
	}
	var out []OpenWebUIInstallation
	for _, item := range items {
		if item.Platform != (OpenWebUI{}).Kind() {
			continue
		}
		installation := OpenWebUIInstallation{ID: item.ID, DisplayName: item.DisplayName, Status: item.Status, LastHeartbeatAt: item.LastHeartbeatAt}
		if health, err := src.InstallationHealth(ctx, item.ID); err == nil {
			installation.KeiProxyVersion = health.RuntimeVersion
			installation.BundleRevision = health.PolicyRevision
			installation.BundleState = health.BundleState
			if health.LastHeartbeatAt != nil {
				installation.LastHeartbeatAt = health.LastHeartbeatAt
			}
		}
		out = append(out, installation)
	}
	return out, nil
}

// WriteOpenWebUIInstallations prints the Open WebUI table, with "-" for any
// field the control plane did not report.
func WriteOpenWebUIInstallations(w io.Writer, items []OpenWebUIInstallation, now time.Time) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "OPEN WEBUI\tID\tSTATUS\tLAST HEARTBEAT\tKEI-PROXY\tBUNDLE REV\tBUNDLE")
	for _, item := range items {
		heartbeat := "-"
		if item.LastHeartbeatAt != nil {
			heartbeat = RelativeTime(now.Sub(*item.LastHeartbeatAt))
		}
		revision := "-"
		if item.BundleRevision != nil {
			revision = strconv.FormatInt(*item.BundleRevision, 10)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", dash(item.DisplayName), item.ID, dash(item.Status), heartbeat, dashPtr(item.KeiProxyVersion), revision, dashPtr(item.BundleState))
	}
	_ = tw.Flush()
}

// RelativeTime formats an age as "42s ago", "5m ago", "3h ago" or "2d ago".
func RelativeTime(age time.Duration) string {
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Minute:
		return strconv.FormatInt(int64(age.Seconds()), 10) + "s ago"
	case age < time.Hour:
		return strconv.FormatInt(int64(age.Minutes()), 10) + "m ago"
	case age < 24*time.Hour:
		return strconv.FormatInt(int64(age.Hours()), 10) + "h ago"
	default:
		return strconv.FormatInt(int64(age.Hours()/24), 10) + "d ago"
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func dashPtr(s *string) string {
	if s == nil {
		return "-"
	}
	return dash(*s)
}
