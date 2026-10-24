package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const testServicePrincipalName = "pedro-bug-intake"

func testServicePrincipalJSON() string {
	return `{"name":"organizations/org-1/servicePrincipals/` + testServicePrincipalName + `","description":"Bug intake","disabled":false,"create_time":"2026-01-01T00:00:00Z","update_time":"2026-01-01T00:00:00Z"}`
}

func TestServicePrincipalsCRUD(t *testing.T) {
	base := "/api/v1/organizations/org-1/servicePrincipals"
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base:
			_, _ = w.Write([]byte(`{"servicePrincipals":[` + testServicePrincipalJSON() + `]}`))
		case r.Method == http.MethodPost && r.URL.Path == base:
			var body createServicePrincipalRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Name != testServicePrincipalName || body.Description != "Bug intake" {
				t.Fatalf("create body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(testServicePrincipalJSON()))
		case r.Method == http.MethodGet && r.URL.Path == base+"/"+testServicePrincipalName:
			_, _ = w.Write([]byte(testServicePrincipalJSON()))
		case r.Method == http.MethodPatch && r.URL.Path == base+"/"+testServicePrincipalName:
			var body updateServicePrincipalRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Description == nil || *body.Description != "Updated" || body.Disabled == nil || *body.Disabled || body.UpdateMask != "description,disabled" {
				t.Fatalf("update body = %#v", body)
			}
			_, _ = w.Write([]byte(testServicePrincipalJSON()))
		case r.Method == http.MethodDelete && r.URL.Path == base+"/"+testServicePrincipalName:
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	})
	var stdout, stderr bytes.Buffer
	if code := runServicePrincipalsList([]string{"--json"}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("list exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), testServicePrincipalName) {
		t.Fatalf("list JSON = %s", stdout.String())
	}
	stdout.Reset()
	if code := runServicePrincipalsCreate([]string{"--name", testServicePrincipalName, "--description", "Bug intake", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("create exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), testServicePrincipalName) {
		t.Fatalf("create output = %s", stdout.String())
	}
	stdout.Reset()
	if code := runServicePrincipalsGet([]string{testServicePrincipalName}, &stdout, &stderr, server.Client(), store); code != 0 {
		t.Fatalf("get exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Bug intake") {
		t.Fatalf("get output = %s", stdout.String())
	}
	stdout.Reset()
	if code := runServicePrincipalsUpdate([]string{testServicePrincipalName, "--description", "Updated", "--enabled", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("update exit=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	if code := runServicePrincipalsDelete([]string{testServicePrincipalName, "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("delete exit=%d stderr=%s", code, stderr.String())
	}
}

func TestServicePrincipalsUpdateDisabledFalse(t *testing.T) {
	server, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("method = %s", r.Method)
		}
		var body updateServicePrincipalRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Disabled == nil || *body.Disabled || body.UpdateMask != "disabled" {
			t.Fatalf("body = %#v", body)
		}
		_, _ = w.Write([]byte(testServicePrincipalJSON()))
	})
	var stdout, stderr bytes.Buffer
	if code := runServicePrincipalsUpdate([]string{testServicePrincipalName, "--enabled", "--yes"}, &stdout, &stderr, strings.NewReader(""), server.Client(), store); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
}

func TestServicePrincipalsWritesRequireConfirmation(t *testing.T) {
	hit := false
	_, store := newUsersGroupsServer(t, func(w http.ResponseWriter, r *http.Request) { hit = true })
	for _, tc := range []struct {
		name string
		run  func(*bytes.Buffer, *bytes.Buffer) int
	}{
		{"create", func(out, errOut *bytes.Buffer) int {
			return runServicePrincipalsCreate([]string{"--name", testServicePrincipalName}, out, errOut, strings.NewReader(""), http.DefaultClient, store)
		}},
		{"update", func(out, errOut *bytes.Buffer) int {
			return runServicePrincipalsUpdate([]string{testServicePrincipalName, "--enabled"}, out, errOut, strings.NewReader(""), http.DefaultClient, store)
		}},
		{"delete", func(out, errOut *bytes.Buffer) int {
			return runServicePrincipalsDelete([]string{testServicePrincipalName}, out, errOut, strings.NewReader(""), http.DefaultClient, store)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := tc.run(&out, &errOut); code != 2 {
				t.Fatalf("exit=%d stderr=%s", code, errOut.String())
			}
		})
	}
	if hit {
		t.Fatal("request made without --yes")
	}
}

func TestServicePrincipalsRejectConflictingStateFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runServicePrincipalsUpdate([]string{testServicePrincipalName, "--enabled", "--disabled", "--yes"}, &stdout, &stderr, strings.NewReader(""), http.DefaultClient, &memoryCredentialStore{}); code != 2 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot be used together") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}
