package app

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

const (
	directorySyncPath     = "/api/v1/organizations/" + testProfileOrgID + "/directory-sync"
	directorySyncRunsPath = directorySyncPath + "/runs"
	testTenantID          = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	testSyncClientID      = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	testSyncAppID         = "cccccccc-cccc-cccc-cccc-cccccccccccc"
)

const testDryRunJSON = `{"id":"r-1","start_time":"2026-10-12T10:00:00Z","end_time":"2026-10-12T10:00:04Z","dry_run":true,"result":"succeeded","error":"",` +
	`"stats":{"would_create":1,"assignments":3,"groups_created":1,"members_added":2,"unknown_users":0},` +
	`"changes":[{"action":"create_group","target":"pilot"},{"action":"add_member","target":"pilot ← ana@dvl.example"}],"changes_truncated":false}`

const testFailedRunJSON = `{"id":"r-0","start_time":"2026-10-12T09:00:00Z","end_time":"2026-10-12T09:00:01Z","dry_run":true,"result":"failed","error":"token: entra returned 401 invalid_client","stats":{},"changes":[]}`

const testDirectorySyncJSON = `{"organization_id":"` + testProfileOrgID + `","provider":"entra","tenant_id":"` + testTenantID + `","client_id":"` + testSyncClientID + `","app_id":"` + testSyncAppID + `",` +
	`"enabled":true,"dry_run":true,"create_users":false,"revoke":false,"version":3,"create_time":"2026-10-12T08:00:00Z","update_time":"2026-10-12T09:30:00Z","last_run":` + testDryRunJSON + `}`

func maskPaths(t *testing.T, body map[string]any) []string {
	t.Helper()
	mask, _ := body["update_mask"].(map[string]any)
	raw, _ := mask["paths"].([]any)
	paths := make([]string, 0, len(raw))
	for _, p := range raw {
		paths = append(paths, p.(string))
	}
	return paths
}

func TestDirectorySyncGet(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testDirectorySyncJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncCommand([]string{"get"}, &stdout, &stderr, nil, http.DefaultClient, store); code != 0 {
		t.Fatalf("get exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	assertRequest(t, got, http.MethodGet, directorySyncPath)
	if got.Query != "" || got.Raw != "" {
		t.Fatalf("get query=%q body=%q", got.Query, got.Raw)
	}
	want := "provider:      entra\n" +
		"tenant_id:     " + testTenantID + "\n" +
		"client_id:     " + testSyncClientID + "\n" +
		"app_id:        " + testSyncAppID + "\n" +
		"group_id:      \n" +
		"enabled:       true\n" +
		"dry_run:       true\n" +
		"create_users:  false\n" +
		"revoke:        false\n" +
		"version:       3\n" +
		"update_time:   2026-10-12T09:30:00Z\n" +
		"last_run:\n" +
		"  start_time:  2026-10-12T10:00:00Z\n" +
		"  result:      succeeded\n" +
		"  dry_run:     true\n" +
		"  stats:       assignments=3 groups_created=1 members_added=2 would_create=1\n" +
		"Run `kei directory-sync report` for the full run.\n"
	if stdout.String() != want {
		t.Fatalf("get output =\n%s\nwant\n%s", stdout.String(), want)
	}
}

func TestDirectorySyncGetShowsLastRunError(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"provider":"entra","last_run":` + testFailedRunJSON + `}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncGet(nil, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("get exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "  error:       token: entra returned 401 invalid_client\n") || !strings.Contains(stdout.String(), "result:      failed") {
		t.Fatalf("get output = %s", stdout.String())
	}
}

func TestDirectorySyncGetWithoutRuns(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"provider":"entra","enabled":false,"dry_run":true,"version":1,"last_run":null}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncGet(nil, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("get exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.HasSuffix(stdout.String(), "last_run: none yet\n") {
		t.Fatalf("get output = %s", stdout.String())
	}
}

func TestDirectorySyncGetJSONPassthrough(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testDirectorySyncJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncGet([]string{"--json"}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("get exit = %d, stderr=%s", code, stderr.String())
	}
	if stdout.String() != testDirectorySyncJSON {
		t.Fatalf("get --json output = %s", stdout.String())
	}
}

func TestDirectorySyncGetNotFound(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"status":"NOT_FOUND","message":"directory sync not found"}}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncGet(nil, &stdout, &stderr, http.DefaultClient, store); code != 1 {
		t.Fatalf("get exit = %d, want 1", code)
	}
	if stderr.String() != "No directory sync configured.\n" || stdout.Len() != 0 {
		t.Fatalf("get stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestDirectorySyncSetCreatesWithMaskOfGivenFlags(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"provider":"entra","tenant_id":"` + testTenantID + `","client_id":"` + testSyncClientID + `","app_id":"` + testSyncAppID + `","enabled":false,"dry_run":true,"version":1,"update_time":"2026-10-12T09:00:00Z","last_run":null}`))
	})
	var stdout, stderr bytes.Buffer
	args := []string{"--tenant-id", testTenantID, "--client-id", testSyncClientID, "--app-id", testSyncAppID, "--yes"}
	if code := runDirectorySyncCommand(append([]string{"set"}, args...), &stdout, &stderr, nil, http.DefaultClient, store); code != 0 {
		t.Fatalf("set exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	assertRequest(t, got, http.MethodPatch, directorySyncPath)
	if got.Query != "" {
		t.Fatalf("set query = %q", got.Query)
	}
	if paths := strings.Join(maskPaths(t, got.Body), ","); paths != "app_id,client_id,tenant_id" {
		t.Fatalf("update_mask = %s", paths)
	}
	if got.Body["tenant_id"] != testTenantID || got.Body["client_id"] != testSyncClientID || got.Body["app_id"] != testSyncAppID {
		t.Fatalf("set body = %#v", got.Body)
	}
	for _, unmasked := range []string{"enabled", "dry_run", "create_users", "revoke"} {
		if _, ok := got.Body[unmasked]; ok {
			t.Fatalf("unmasked %s sent: %s", unmasked, got.Raw)
		}
	}
	if !strings.Contains(stdout.String(), "tenant_id:     "+testTenantID) || !strings.Contains(stdout.String(), "last_run: none yet") {
		t.Fatalf("set output = %s", stdout.String())
	}
}

func TestDirectorySyncSetWithGroupID(t *testing.T) {
	const entraGroupID = "dddddddd-dddd-dddd-dddd-dddddddddddd"
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"provider":"entra","tenant_id":"` + testTenantID + `","client_id":"` + testSyncClientID + `","group_id":"` + entraGroupID + `","enabled":false,"dry_run":true,"version":1,"last_run":null}`))
	})
	var stdout, stderr bytes.Buffer
	args := []string{"--group-id", entraGroupID, "--dry-run=true", "--yes"}
	if code := runDirectorySyncSet(args, &stdout, &stderr, nil, http.DefaultClient, store); code != 0 {
		t.Fatalf("set exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	assertRequest(t, got, http.MethodPatch, directorySyncPath)
	if paths := strings.Join(maskPaths(t, got.Body), ","); paths != "dry_run,group_id" {
		t.Fatalf("update_mask = %s", paths)
	}
	if got.Body["group_id"] != entraGroupID {
		t.Fatalf("group_id absent from request: body=%s", got.Raw)
	}
	if !strings.Contains(stdout.String(), "group_id:      "+entraGroupID) {
		t.Fatalf("set output omitted group_id: %s", stdout.String())
	}
}

func TestDirectorySyncSetConfirmationShowsGroupID(t *testing.T) {
	const entraGroupID = "dddddddd-dddd-dddd-dddd-dddddddddddd"
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testDirectorySyncJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncSet([]string{"--group-id", entraGroupID}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 2 {
		t.Fatalf("set exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "Set directory sync group_id="+entraGroupID+"?") {
		t.Fatalf("confirmation omitted group_id: %q", stderr.String())
	}
	if len(fake.all()) != 0 {
		t.Fatal("set sent a request without --yes")
	}
}

func TestDirectorySyncSetBooleansSendFalse(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testDirectorySyncJSON))
	})
	var stdout, stderr bytes.Buffer
	args := []string{"--dry-run=false", "--create-users=true", "--revoke=false", "--yes"}
	if code := runDirectorySyncSet(args, &stdout, &stderr, nil, http.DefaultClient, store); code != 0 {
		t.Fatalf("set exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	assertRequest(t, got, http.MethodPatch, directorySyncPath)
	if paths := strings.Join(maskPaths(t, got.Body), ","); paths != "create_users,dry_run,revoke" {
		t.Fatalf("update_mask = %s", paths)
	}
	if got.Body["dry_run"] != false || got.Body["create_users"] != true || got.Body["revoke"] != false {
		t.Fatalf("set body = %s", got.Raw)
	}
	for _, unmasked := range []string{"tenant_id", "client_id", "app_id", "enabled"} {
		if _, ok := got.Body[unmasked]; ok {
			t.Fatalf("unmasked %s sent: %s", unmasked, got.Raw)
		}
	}
}

func TestDirectorySyncSetUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no flags", nil, "requires at least one of"},
		{"only --yes", []string{"--yes"}, "requires at least one of"},
		{"bad bool", []string{"--dry-run=maybe", "--yes"}, "invalid boolean value"},
		{"bad group ID", []string{"--group-id", "not-a-guid", "--yes"}, "--group-id must be a GUID"},
		{"app and group IDs", []string{"--app-id", testSyncAppID, "--group-id", "dddddddd-dddd-dddd-dddd-dddddddddddd", "--yes"}, "--group-id and --app-id are mutually exclusive"},
		{"positional", []string{"extra"}, "takes no positional arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
				_, _ = w.Write([]byte(testDirectorySyncJSON))
			})
			var stdout, stderr bytes.Buffer
			if code := runDirectorySyncSet(tt.args, &stdout, &stderr, nil, http.DefaultClient, store); code != 2 {
				t.Fatalf("set %v exit = %d, want 2", tt.args, code)
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("set %v stderr = %q", tt.args, stderr.String())
			}
			if n := len(fake.all()); n != 0 {
				t.Fatalf("set %v sent %d requests", tt.args, n)
			}
		})
	}
}

func TestDirectorySyncSetWithoutYesDoesNotWrite(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testDirectorySyncJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncSet([]string{"--dry-run=false"}, &stdout, &stderr, strings.NewReader("y\n"), http.DefaultClient, store); code != 2 {
		t.Fatalf("set exit = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "Set directory sync dry_run=false?") {
		t.Fatalf("set stderr = %q", stderr.String())
	}
	if n := len(fake.all()); n != 0 {
		t.Fatalf("set sent %d requests without confirmation", n)
	}
}

func TestDirectorySyncSetServerError(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":409,"status":"ALREADY_EXISTS","message":"tenant is already synced by another organization"}}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncSet([]string{"--tenant-id", testTenantID, "--yes"}, &stdout, &stderr, nil, http.DefaultClient, store); code != 1 {
		t.Fatalf("set exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "already synced") || !strings.Contains(stderr.String(), "HTTP 409") {
		t.Fatalf("set stderr = %q", stderr.String())
	}
}

func TestDirectorySyncEnable(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testDirectorySyncJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncCommand([]string{"enable", "--yes"}, &stdout, &stderr, nil, http.DefaultClient, store); code != 0 {
		t.Fatalf("enable exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	assertRequest(t, got, http.MethodPatch, directorySyncPath)
	if got.Raw != `{"enabled":true,"update_mask":{"paths":["enabled"]}}` {
		t.Fatalf("enable body = %s", got.Raw)
	}
	want := "Directory sync enabled. The bridge syncs this organization at its next interval.\n" +
		"It runs in dry run (the default): nothing is written; each run records what it would change.\n" +
		"Read the newest run with `kei directory-sync report`, then turn dry run off with `kei directory-sync set --dry-run=false`.\n"
	if stdout.String() != want {
		t.Fatalf("enable output = %q", stdout.String())
	}
}

func TestDirectorySyncEnableLive(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"provider":"entra","enabled":true,"dry_run":false}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncCommand([]string{"enable", "--yes"}, &stdout, &stderr, nil, http.DefaultClient, store); code != 0 {
		t.Fatalf("enable exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Dry run is off") {
		t.Fatalf("enable output = %q", stdout.String())
	}
}

func TestDirectorySyncDisable(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"provider":"entra","enabled":false,"dry_run":true}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncCommand([]string{"disable", "--yes"}, &stdout, &stderr, nil, http.DefaultClient, store); code != 0 {
		t.Fatalf("disable exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	assertRequest(t, got, http.MethodPatch, directorySyncPath)
	if got.Raw != `{"enabled":false,"update_mask":{"paths":["enabled"]}}` {
		t.Fatalf("disable body = %s", got.Raw)
	}
	if stdout.String() != "Directory sync disabled. The bridge stops syncing this organization at its next interval.\n" {
		t.Fatalf("disable output = %q", stdout.String())
	}
}

func TestDirectorySyncEnableRequiresConfirmation(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(testDirectorySyncJSON))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncCommand([]string{"enable"}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, store); code != 2 {
		t.Fatalf("enable exit = %d, want 2", code)
	}
	if n := len(fake.all()); n != 0 {
		t.Fatalf("enable sent %d requests without confirmation", n)
	}
}

func TestDirectorySyncReportNewestRunInFull(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"items":[` + testDryRunJSON + `],"next_page_token":"abc"}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncCommand([]string{"report"}, &stdout, &stderr, nil, http.DefaultClient, store); code != 0 {
		t.Fatalf("report exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	assertRequest(t, got, http.MethodGet, directorySyncRunsPath)
	if got.Query != "page_size=1" {
		t.Fatalf("report query = %q", got.Query)
	}
	want := "start_time:  2026-10-12T10:00:00Z\n" +
		"end_time:    2026-10-12T10:00:04Z\n" +
		"result:      succeeded\n" +
		"dry_run:     true\n" +
		"stats:\n" +
		"  assignments:     3\n" +
		"  groups_created:  1\n" +
		"  members_added:   2\n" +
		"  unknown_users:   0\n" +
		"  would_create:    1\n" +
		"changes (dry run; not applied):\n" +
		"  create_group → pilot\n" +
		"  add_member → pilot ← ana@dvl.example\n"
	if stdout.String() != want {
		t.Fatalf("report output =\n%s\nwant\n%s", stdout.String(), want)
	}
}

func TestDirectorySyncReportFailedTruncatedRun(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"runs":[{"start_time":"t0","result":"failed","dry_run":false,"error":"remove cap exceeded","changes":[{"action":"remove_member","target":"pilot ← bo@dvl.example"}],"changes_truncated":true}]}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncReport(nil, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("report exit = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"error:       remove cap exceeded\n", "changes:\n  remove_member → pilot ← bo@dvl.example\n", "Only the first 1 changes are listed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report output missing %q:\n%s", want, out)
		}
	}
}

func TestDirectorySyncReportTable(t *testing.T) {
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"items":[` + testDryRunJSON + `,` + testFailedRunJSON + `]}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncReport([]string{"--limit", "5"}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("report exit = %d, stderr=%s", code, stderr.String())
	}
	got := fake.only(t)
	assertRequest(t, got, http.MethodGet, directorySyncRunsPath)
	if got.Query != "page_size=5" {
		t.Fatalf("report query = %q", got.Query)
	}
	want := "START_TIME            RESULT     DRY_RUN  GROUPS_CREATED  MEMBERS_ADDED  REVOKED  USERS_CREATED  WOULD_CREATE  ERROR\n" +
		"2026-10-12T10:00:00Z  succeeded  true     1               2              0        0              1             \n" +
		"2026-10-12T09:00:00Z  failed     true     0               0              0        0              0             token: entra returned 401 invalid_client\n"
	if stdout.String() != want {
		t.Fatalf("report table =\n%s\nwant\n%s", stdout.String(), want)
	}
}

func TestDirectorySyncReportNoRuns(t *testing.T) {
	_, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncReport(nil, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("report exit = %d, stderr=%s", code, stderr.String())
	}
	if stdout.String() != "No runs yet.\n" {
		t.Fatalf("report output = %q", stdout.String())
	}
}

func TestDirectorySyncReportJSONPassthrough(t *testing.T) {
	const response = `{"items":[` + testFailedRunJSON + `]}`
	fake, store := newProfileConsole(t, func(w http.ResponseWriter, r consoleRequest) {
		_, _ = w.Write([]byte(response))
	})
	var stdout, stderr bytes.Buffer
	if code := runDirectorySyncReport([]string{"--json", "--limit", "3"}, &stdout, &stderr, http.DefaultClient, store); code != 0 {
		t.Fatalf("report exit = %d, stderr=%s", code, stderr.String())
	}
	if got := fake.only(t); got.Query != "page_size=3" {
		t.Fatalf("report query = %q", got.Query)
	}
	if stdout.String() != response {
		t.Fatalf("report --json output = %s", stdout.String())
	}
}

func TestDirectorySyncReportBadLimit(t *testing.T) {
	for _, args := range [][]string{{"--limit", "0"}, {"--limit", "x"}} {
		var stdout, stderr bytes.Buffer
		if code := runDirectorySyncReport(args, &stdout, &stderr, http.DefaultClient, &memoryCredentialStore{}); code != 2 {
			t.Fatalf("report %v exit = %d, want 2", args, code)
		}
	}
}

func TestDirectorySyncCommandDispatch(t *testing.T) {
	tests := []struct {
		args []string
		code int
	}{
		{[]string{}, 0}, // help to stdout
		{[]string{"unknown"}, 2},
		{[]string{"get"}, 1},             // no token → auth fail
		{[]string{"set"}, 2},             // no field
		{[]string{"enable", "--yes"}, 1}, // no token → auth fail
		{[]string{"disable", "x"}, 2},    // positional
		{[]string{"report"}, 1},          // no token → auth fail
	}
	store := &memoryCredentialStore{}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		if code := runDirectorySyncCommand(tt.args, &stdout, &stderr, nil, http.DefaultClient, store); code != tt.code {
			t.Errorf("directory-sync %v exit = %d, want %d (stderr=%s)", tt.args, code, tt.code, stderr.String())
		}
	}
}

func TestPrintUsageListsDirectorySync(t *testing.T) {
	var buf bytes.Buffer
	PrintUsage(&buf)
	if !strings.Contains(buf.String(), "kei directory-sync get [--json] | set [--tenant-id G]") {
		t.Fatalf("PrintUsage omitted directory-sync:\n%s", buf.String())
	}
}
