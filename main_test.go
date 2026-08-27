package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type memoryCredentialStore struct {
	server string
	token  string
}

func (s *memoryCredentialStore) Save(serverURL, token string) error {
	s.server = serverURL
	s.token = token
	return nil
}

func TestLoginStoresTokenWithoutPrintingIt(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/cli/device/authorize":
			json.NewEncoder(w).Encode(deviceAuthorizationStartResponse{
				DeviceCode:      "device-secret",
				UserCode:        "ABCDE-23456",
				ExpiresAt:       time.Now().Add(time.Minute),
				IntervalSeconds: 1,
			})
		case "/api/cli/device/token":
			polls++
			if polls == 1 {
				json.NewEncoder(w).Encode(deviceAuthorizationPollResponse{Status: "pending"})
				return
			}
			json.NewEncoder(w).Encode(deviceAuthorizationPollResponse{Status: "approved", AccessToken: "never-print-this-token", OrgID: "org-123"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var output bytes.Buffer
	store := &memoryCredentialStore{}
	sleeps := 0
	err := login(context.Background(), server.URL, "kei-cli@test", &output, server.Client(), store, func(time.Duration) { sleeps++ })
	if err != nil {
		t.Fatal(err)
	}
	if store.token != "never-print-this-token" || store.server != server.URL {
		t.Fatalf("token was not saved to credential store: %#v", store)
	}
	if strings.Contains(output.String(), "never-print-this-token") || strings.Contains(output.String(), "device-secret") {
		t.Fatalf("login output exposed a secret: %q", output.String())
	}
	if sleeps != 2 {
		t.Fatalf("sleep count = %d, want 2", sleeps)
	}
}

func TestNormalizedKeiWebURL(t *testing.T) {
	if _, err := normalizedKeiWebURL("not-a-url"); err == nil {
		t.Fatal("expected relative URL to be rejected")
	}
	if _, err := normalizedKeiWebURL("https://kei.example.test?bad=true"); err == nil {
		t.Fatal("expected URL query to be rejected")
	}
	if got, err := normalizedKeiWebURL("https://kei.example.test/"); err != nil || got != "https://kei.example.test" {
		t.Fatalf("normalized URL = %q, %v", got, err)
	}
}
