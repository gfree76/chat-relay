//go:build integration

package main

import (
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
	ts := httptest.NewServer(newServer(newStore(), fp.push).routes())
	defer ts.Close()

	post := func(path, body string) *http.Response {
		t.Helper()
		resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp
	}

	if resp := post("/register", `{"userId":"wife","fcmToken":"wife-tok"}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("register: got %d, want 204", resp.StatusCode)
	}
	if resp := post("/send", `{"toUserId":"wife","ciphertext":"Q0lQSEVS"}`); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send: got %d, want 202", resp.StatusCode)
	}
	if len(fp.calls) != 1 || fp.calls[0].token != "wife-tok" || fp.calls[0].ciphertext != "Q0lQSEVS" {
		t.Fatalf("push not delivered end-to-end: %+v", fp.calls)
	}
}
