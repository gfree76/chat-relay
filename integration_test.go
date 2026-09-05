//go:build integration

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRoundTrip exercises the full HTTP stack: a real client hitting a real
// server over TCP, register then send, asserting the push reached the right
// token end to end. Run with: go test -tags integration -run RoundTrip
func TestRoundTrip(t *testing.T) {
	fp := &fakePusher{}
	ts := httptest.NewServer(newServer(testStore(t), fp.push).routes())
	defer ts.Close()

	post := func(path, body, token string) *http.Response {
		t.Helper()
		req, err := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatalf("new request %s: %v", path, err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp
	}

	authToken := func(resp *http.Response) string {
		t.Helper()
		defer resp.Body.Close()
		var out registerResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode register response: %v", err)
		}
		return out.AuthToken
	}

	resp := post("/register", `{"userId":"geoff","fcmToken":"geoff-tok"}`, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("register sender: got %d, want 200", resp.StatusCode)
	}
	sender := authToken(resp)

	if resp := post("/register", `{"userId":"wife","fcmToken":"wife-tok"}`, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("register recipient: got %d, want 200", resp.StatusCode)
	}
	if resp := post("/send", `{"toUserId":"wife","ciphertext":"Q0lQSEVS"}`, sender); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send: got %d, want 202", resp.StatusCode)
	}
	if len(fp.calls) != 1 || fp.calls[0].token != "wife-tok" || fp.calls[0].ciphertext != "Q0lQSEVS" {
		t.Fatalf("push not delivered end-to-end: %+v", fp.calls)
	}

	if resp := post("/send", `{"toUserId":"wife","ciphertext":"Q0lQSEVS"}`, ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated send: got %d, want 401", resp.StatusCode)
	}
}
