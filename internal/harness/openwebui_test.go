package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testOpenWebUIToken = "sk-owui-admin-secret"

// fakeOpenWebUI serves GET /api/v1/functions/ for the admin token only.
func fakeOpenWebUI(t *testing.T, functions string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/functions/" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testOpenWebUIToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"bad token ` + r.Header.Get("Authorization") + `"}`))
			return
		}
		_, _ = w.Write([]byte(functions))
	}))
	t.Cleanup(server.Close)
	return server
}

func checkOpenWebUIText(t *testing.T, functions string) (OpenWebUIReport, string) {
	t.Helper()
	server := fakeOpenWebUI(t, functions)
	report, err := OpenWebUI{BaseURL: server.URL}.CheckFunctions(t.Context(), server.Client(), testOpenWebUIToken)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	var out bytes.Buffer
	report.WriteText(&out)
	if strings.Contains(out.String(), testOpenWebUIToken) {
		t.Fatalf("token printed: %q", out.String())
	}
	return report, out.String()
}

func TestOpenWebUIFunctionsInSync(t *testing.T) {
	report, out := checkOpenWebUIText(t, `[{"id":"kei","type":"pipe","is_active":true,"is_global":false},{"id":"kei_guard","type":"filter","is_active":true,"is_global":true},{"id":"other","is_active":false}]`)
	if report.Drift() {
		t.Fatalf("drift reported: %q", out)
	}
	if !strings.Contains(out, "Kei pipe (kei): ok") || !strings.Contains(out, "kei_guard filter (kei_guard): ok") || !strings.Contains(out, "In sync") {
		t.Fatalf("output = %q", out)
	}
}

func TestOpenWebUIFunctionsMissing(t *testing.T) {
	report, out := checkOpenWebUIText(t, `[{"id":"kei","is_active":true}]`)
	if !report.Drift() || !strings.Contains(out, "(kei_guard): missing") || !strings.Contains(out, "restart the Open WebUI container") {
		t.Fatalf("drift=%v output=%q", report.Drift(), out)
	}
}

func TestOpenWebUIFunctionsInactiveOrNotGlobal(t *testing.T) {
	report, out := checkOpenWebUIText(t, `[{"id":"kei","is_active":false},{"id":"kei_guard","is_active":true,"is_global":false}]`)
	if !report.Drift() || !strings.Contains(out, "(kei): inactive") || !strings.Contains(out, "(kei_guard): not global") {
		t.Fatalf("drift=%v output=%q", report.Drift(), out)
	}
}

func TestOpenWebUIErrorsNeverCarryToken(t *testing.T) {
	server := fakeOpenWebUI(t, `[]`)
	_, err := OpenWebUI{BaseURL: server.URL}.CheckFunctions(t.Context(), server.Client(), "sk-wrong-secret")
	if err == nil || strings.Contains(err.Error(), "sk-wrong-secret") || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("err = %v", err)
	}
	for _, base := range []string{"http://example.com", "https://user:pw@example.com", "not a url"} {
		if _, err := (OpenWebUI{BaseURL: base}).CheckFunctions(t.Context(), server.Client(), testOpenWebUIToken); err == nil {
			t.Errorf("%s accepted", base)
		}
	}
	if _, err := (OpenWebUI{BaseURL: server.URL}).CheckFunctions(t.Context(), server.Client(), ""); err == nil {
		t.Error("empty token accepted")
	}
}

func TestOpenWebUIHarnessHasRemoteTargetAndRendersNothing(t *testing.T) {
	h, ok := Default.Get("openwebui")
	if !ok || h.Detect(hermeticEnv(t.TempDir(), nil)) {
		t.Fatalf("openwebui registered=%v and must never be auto-detected", ok)
	}
	targets, err := OpenWebUI{BaseURL: "https://chat.example.com/"}.Targets(Env{})
	if err != nil || len(targets) != 1 || !targets[0].Remote() || targets[0].URL != "https://chat.example.com/api/v1/functions/" {
		t.Fatalf("targets = %+v err=%v", targets, err)
	}
	if _, err := h.ImportRules(Env{}, ""); !errors.Is(err, ErrNoNativeStore) {
		t.Fatalf("import err = %v", err)
	}
	policies, _ := json.Marshal(map[string]any{"policies": []map[string]any{
		{"id": "p1", "name": "allow model", "src_pattern": "*", "dst_pattern": "tool:model.invoke", "effect": "permit", "enabled": true},
		{"id": "p2", "name": "claude only", "src_pattern": "harness:claude_code", "dst_pattern": "shell:git", "effect": "permit", "enabled": true},
		{"id": "p3", "name": "disabled", "src_pattern": "*", "dst_pattern": "*", "effect": "deny", "enabled": false},
	}})
	r, err := h.Render(Bundle{PolicySet: policies})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Allows)+len(r.Denies) != 0 || strings.Join(r.DecidedLive, ",") != "allow model" {
		t.Fatalf("rendered = %+v", r)
	}
	result, err := h.Apply(t.Context(), r, ApplyOpts{})
	if err != nil || !result.Skipped {
		t.Fatalf("apply = %+v err=%v", result, err)
	}
}
