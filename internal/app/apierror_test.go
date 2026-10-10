package app

import (
	"testing"
)

func TestAPIErrorMessage(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    string
	}{
		{
			name:    "AIP-193 wrapped with ErrorInfo reason",
			payload: []byte(`{"error":{"code":3,"status":"INVALID_ARGUMENT","message":"effect must be permit or deny","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"INVALID_ARGUMENT","domain":"haikeilabs.com","metadata":{}}]}}`),
			want:    "INVALID_ARGUMENT: effect must be permit or deny",
		},
		{
			name:    "AIP-193 wrapped without details (just message)",
			payload: []byte(`{"error":{"code":3,"status":"INVALID_ARGUMENT","message":"something went wrong"}}`),
			want:    "something went wrong",
		},
		{
			name:    "AIP-193 wrapped with empty details array",
			payload: []byte(`{"error":{"code":3,"message":"no details","details":[]}}`),
			want:    "no details",
		},
		{
			name:    "flat AIP reason and message",
			payload: []byte(`{"reason":"invalid_argument","message":"effect must be permit or deny"}`),
			want:    "invalid_argument: effect must be permit or deny",
		},
		{
			name:    "flat AIP message only (no reason)",
			payload: []byte(`{"message":"some error occurred"}`),
			want:    "some error occurred",
		},
		{
			name:    "legacy structured error",
			payload: []byte(`{"error":{"message":"legacy error"}}`),
			want:    "legacy error",
		},
		{
			name:    "RFC 6749 error string",
			payload: []byte(`{"error":"invalid_request"}`),
			want:    "invalid_request",
		},
		{
			name:    "plain text",
			payload: []byte(`not json`),
			want:    "not json",
		},
		{
			name:    "empty payload",
			payload: []byte(``),
			want:    "request failed",
		},
		{
			name:    "JSON with no recognized fields",
			payload: []byte(`{"foo":"bar"}`),
			want:    `{"foo":"bar"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := apiErrorMessage(tc.payload)
			if got != tc.want {
				t.Errorf("apiErrorMessage(%q) = %q, want %q", string(tc.payload), got, tc.want)
			}
		})
	}
}
