package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/HaikeiLabs/kei-connector-contracts/harnessmatch"
)

// OpenWebUI is the Open WebUI harness (platform openwebui). Its native store
// is remote: the functions table behind Open WebUI's admin API. The CLI never
// writes there. kei-openwebui upserts its functions on every boot and
// overwrites drift, and the in-container kei-proxy decides every call live, so
// sync only reports what Open WebUI holds.
type OpenWebUI struct {
	// BaseURL is the Open WebUI base URL (https://chat.example.com). Empty
	// in the default registry; `kei harness sync --url` supplies it.
	BaseURL string
}

// OpenWebUIFunction is a function kei-openwebui seeds into Open WebUI. The ids
// mirror FUNCTIONS in kei-openwebui src/kei_openwebui/seed.py.
type OpenWebUIFunction struct {
	ID     string
	Name   string
	Global bool
}

// OpenWebUIFunctions are the functions every kei-openwebui boot seeds: the
// Kei pipe and the kei_guard global filter.
var OpenWebUIFunctions = []OpenWebUIFunction{
	{ID: "kei", Name: "Kei pipe"},
	{ID: "kei_guard", Name: "kei_guard filter", Global: true},
}

const openWebUIFunctionsPath = "/api/v1/functions/"

func (OpenWebUI) Kind() string                            { return "openwebui" }
func (OpenWebUI) Detect(Env) bool                         { return false }
func (OpenWebUI) HookSpec(Env) *HookSpec                  { return nil }
func (OpenWebUI) ImportRules(Env, string) ([]Rule, error) { return nil, ErrNoNativeStore }

// Targets is the Open WebUI functions API, or nothing when no URL is set.
func (o OpenWebUI) Targets(Env) ([]Target, error) {
	if o.BaseURL == "" {
		return nil, nil
	}
	return []Target{{URL: strings.TrimRight(o.BaseURL, "/") + openWebUIFunctionsPath}}, nil
}

// Render returns no native entries: every policy that applies to Open WebUI
// is listed in DecidedLive, because kei-proxy decides it at call time.
func (OpenWebUI) Render(b Bundle) (Rendered, error) {
	var set struct {
		Policies []bundlePolicy `json:"policies"`
	}
	if len(b.PolicySet) > 0 {
		if err := json.Unmarshal(b.PolicySet, &set); err != nil {
			return Rendered{}, fmt.Errorf("decode bundle policies: %w", err)
		}
	}
	r := Rendered{Kind: "openwebui", BundleVersion: b.BundleVersion, BundleDigest: b.PayloadDigest, NotAfter: b.NotAfter}
	call := harnessmatch.Call{Kind: "openwebui"}
	for _, policy := range set.Policies {
		effect := policy.Effect
		if effect == "" {
			effect = policy.Action
		}
		if !policy.Enabled || (effect != "permit" && effect != "deny") {
			continue
		}
		if applies, _ := harnessmatch.MatchSrc(policy.SrcPattern, call); applies {
			r.DecidedLive = append(r.DecidedLive, policyDisplayName(policy))
		}
	}
	return r, nil
}

func (OpenWebUI) Apply(context.Context, Rendered, ApplyOpts) (Result, error) {
	return Result{Skipped: true, Notes: []string{"openwebui harness: skipped; kei-openwebui seeds its functions on every boot and kei-proxy decides every call live. Use --url to report the Open WebUI functions."}}, nil
}

// OpenWebUIFunctionStatus is what Open WebUI holds for one seeded function.
type OpenWebUIFunctionStatus struct {
	OpenWebUIFunction
	Present  bool
	Active   bool
	IsGlobal bool
}

// Drifted reports whether the function is missing, inactive, or (for a
// global filter) not global.
func (s OpenWebUIFunctionStatus) Drifted() bool {
	return !s.Present || !s.Active || s.Global && !s.IsGlobal
}

// OpenWebUIReport is the read-only state of the seeded functions.
type OpenWebUIReport struct {
	Functions []OpenWebUIFunctionStatus
}

// Drift reports whether any seeded function has drifted.
func (r OpenWebUIReport) Drift() bool {
	for _, f := range r.Functions {
		if f.Drifted() {
			return true
		}
	}
	return false
}

// WriteText prints one line per seeded function and, on drift, the remedy.
func (r OpenWebUIReport) WriteText(w io.Writer) {
	for _, f := range r.Functions {
		state := "ok"
		switch {
		case !f.Present:
			state = "missing"
		case !f.Active:
			state = "inactive"
		case f.Global && !f.IsGlobal:
			state = "not global"
		}
		fmt.Fprintf(w, "%s (%s): %s\n", f.Name, f.ID, state)
	}
	if r.Drift() {
		fmt.Fprintln(w, "Drift: kei-openwebui re-seeds its functions on every boot; restart the Open WebUI container to restore them.")
	} else {
		fmt.Fprintln(w, "In sync: kei-proxy decides every call live.")
	}
}

// CheckFunctions reads Open WebUI's functions list with an admin token and
// reports the seeded functions. It never writes, and token never appears in
// a returned error.
func (o OpenWebUI) CheckFunctions(ctx context.Context, client *http.Client, token string) (OpenWebUIReport, error) {
	base, err := url.Parse(o.BaseURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return OpenWebUIReport{}, errors.New("--url must be an absolute http(s) URL without credentials, query or fragment")
	}
	if !strings.EqualFold(base.Scheme, "https") && !(strings.EqualFold(base.Scheme, "http") && loopbackHost(base.Hostname())) {
		return OpenWebUIReport{}, errors.New("--url must use https (http only for localhost)")
	}
	if token == "" {
		return OpenWebUIReport{}, errors.New("OPENWEBUI_ADMIN_TOKEN is not set")
	}
	targets, _ := o.Targets(Env{})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targets[0].URL, nil)
	if err != nil {
		return OpenWebUIReport{}, errors.New("build Open WebUI request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return OpenWebUIReport{}, fmt.Errorf("list Open WebUI functions: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return OpenWebUIReport{}, fmt.Errorf("Open WebUI rejected OPENWEBUI_ADMIN_TOKEN (HTTP %d); it must be an admin API key", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return OpenWebUIReport{}, fmt.Errorf("list Open WebUI functions returned HTTP %d", resp.StatusCode)
	}
	var rows []struct {
		ID       string `json:"id"`
		IsActive bool   `json:"is_active"`
		IsGlobal bool   `json:"is_global"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&rows); err != nil {
		return OpenWebUIReport{}, errors.New("decode Open WebUI functions list")
	}
	var report OpenWebUIReport
	for _, fn := range OpenWebUIFunctions {
		status := OpenWebUIFunctionStatus{OpenWebUIFunction: fn}
		for _, row := range rows {
			if row.ID == fn.ID {
				status.Present, status.Active, status.IsGlobal = true, row.IsActive, row.IsGlobal
				break
			}
		}
		report.Functions = append(report.Functions, status)
	}
	return report, nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
