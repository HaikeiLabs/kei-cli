package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const (
	testServicePrincipalID = "dddddddd-1111-2222-3333-444444444444"
	testBindingID          = "eeeeeeee-1111-2222-3333-444444444444"
	testInstallationID     = "ffffffff-1111-2222-3333-444444444444"
	testSPAgentID          = "aaaaaaaa-2222-3333-4444-555555555555"
)

func principalListJSON() string {
	return `{"servicePrincipals":[{"id":"` + testServicePrincipalID + `","name":"organizations/org-1/servicePrincipals/` + testServicePrincipalName + `"}]}`
}

func bindingJSON() string {
	return `{"id":"` + testBindingID + `","service_principal_id":"` + testServicePrincipalID + `","installation_id":"` + testInstallationID + `","agent_id":"` + testSPAgentID + `"}`
}

func TestServicePrincipalBindUnbindAndBindingsList(t *testing.T) {
	bindingPath := "/api/v1/organizations/org-1/workspaces/" + testWorkspaceID + "/service-principal-bindings"
	principalIDRead := false
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/organizations/org-1/servicePrincipals":
			principalIDRead = true
			_, _ = w.Write([]byte(principalListJSON()))
		case r.Method == http.MethodPost && r.URL.Path == bindingPath:
			var body servicePrincipalBinding
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.ServicePrincipalID != testServicePrincipalID || body.InstallationID != testInstallationID || body.AgentID != testSPAgentID {
				t.Fatalf("binding create body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(bindingJSON()))
		case r.Method == http.MethodGet && r.URL.Path == bindingPath:
			_, _ = w.Write([]byte(`{"service_principal_bindings":[` + bindingJSON() + `]}`))
		case r.Method == http.MethodDelete && r.URL.Path == bindingPath+"/"+testBindingID:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})
	var stdout, stderr bytes.Buffer
	if code := runServicePrincipalBind([]string{testServicePrincipalName, "--installation", testInstallationID, "--agent", testSPAgentID, "--workspace", "Main", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("bind exit=%d stderr=%s", code, stderr.String())
	}
	if !principalIDRead || !strings.Contains(stdout.String(), testBindingID) {
		t.Fatalf("principal lookup=%t output=%s", principalIDRead, stdout.String())
	}
	stdout.Reset()
	if code := runServicePrincipalBindingsCommand([]string{"list", "--workspace", "Main", "--json"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("bindings list exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), testBindingID) {
		t.Fatalf("bindings JSON=%s", stdout.String())
	}
	stdout.Reset()
	if code := runServicePrincipalUnbind([]string{"--binding-id", testBindingID, "--workspace", "Main", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("unbind by ID exit=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	if code := runServicePrincipalUnbind([]string{testServicePrincipalName, "--installation", testInstallationID, "--agent", testSPAgentID, "--workspace", "Main", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("unbind by pair exit=%d stderr=%s", code, stderr.String())
	}
}

func TestServicePrincipalGroupMembershipUsesGroupsMembersStyle(t *testing.T) {
	memberPath := "/api/v1/organizations/org-1/workspaces/" + testWorkspaceID + "/groups/" + testGroupID + "/service-principals"
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups":
			_, _ = w.Write([]byte(`[{"id":"` + testGroupID + `","name":"Support","users":[]}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/organizations/org-1/servicePrincipals":
			_, _ = w.Write([]byte(principalListJSON()))
		case r.Method == http.MethodPost && r.URL.Path == memberPath:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["service_principal_id"] != testServicePrincipalID {
				t.Fatalf("membership body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"service_principal_id":"` + testServicePrincipalID + `"}`))
		case r.Method == http.MethodDelete && r.URL.Path == memberPath+"/"+testServicePrincipalID:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})
	var stdout, stderr bytes.Buffer
	args := []string{"Support", "--service-principal", testServicePrincipalName, "--workspace", "Main", "--yes"}
	if code := runGroupsMembersAdd(args, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("group member add exit=%d stderr=%s", code, stderr.String())
	}
	if code := runGroupsMembersRemove(args, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("group member remove exit=%d stderr=%s", code, stderr.String())
	}
}

func TestServicePrincipalBindValidatesIDsAndConfirmation(t *testing.T) {
	for _, args := range [][]string{
		{testServicePrincipalName, "--installation", "bad", "--agent", testSPAgentID, "--workspace", "Main", "--yes"},
		{testServicePrincipalName, "--installation", testInstallationID, "--agent", testSPAgentID, "--workspace", "Main"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runServicePrincipalBind(args, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, &memoryCredentialStore{}); code != 2 {
			t.Fatalf("args=%v exit=%d stderr=%s", args, code, stderr.String())
		}
	}
}
