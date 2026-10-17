package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakePusher records push calls so tests can assert delivery without real FCM.
type fakePusher struct {
	calls []pushCall
	err   error
}

type pushCall struct{ token, ciphertext string }

func (f *fakePusher) push(token, ciphertext string) error {
	f.calls = append(f.calls, pushCall{token, ciphertext})
	return f.err
}

func newTestServer(t *testing.T) (*server, *fakePusher) {
	t.Helper()
	fp := &fakePusher{}
	return newServer(testStore(t), fp.push), fp
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doAuth(t, h, method, path, body, "")
}

func doAuth(t *testing.T, h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// registerUser claims a userId through the HTTP surface and returns its auth token.
func registerUser(t *testing.T, h http.Handler, userID, fcmToken string) string {
	t.Helper()
	rec := do(t, h, "POST", "/register", `{"userId":"`+userID+`","fcmToken":"`+fcmToken+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("register %s: got %d, want 200", userID, rec.Code)
	}
	var resp registerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if resp.AuthToken == "" {
		t.Fatal("register returned an empty auth token")
	}
	return resp.AuthToken
}

func TestRegisterHandler(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.routes()

	registerUser(t, h, "geoff", "tok")
	if _, ok, _ := srv.store.lookup("geoff"); !ok {
		t.Fatal("user not stored after register")
	}
	if rec := do(t, h, "POST", "/register", `{"userId":"","fcmToken":""}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing fields: got %d, want 400", rec.Code)
	}
	if rec := do(t, h, "POST", "/register", `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: got %d, want 400", rec.Code)
	}
	if rec := do(t, h, "GET", "/register", ``); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method: got %d, want 405", rec.Code)
	}
}

func TestRegisterRefreshRequiresOwnership(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.routes()
	token := registerUser(t, h, "geoff", "tok1")

	body := `{"userId":"geoff","fcmToken":"tok2"}`
	if rec := do(t, h, "POST", "/register", body); rec.Code != http.StatusForbidden {
		t.Fatalf("reclaim without token: got %d, want 403", rec.Code)
	}
	if rec := doAuth(t, h, "POST", "/register", body, "wrong-token"); rec.Code != http.StatusForbidden {
		t.Fatalf("reclaim with wrong token: got %d, want 403", rec.Code)
	}
	if got, _, _ := srv.store.lookup("geoff"); got.FCMToken != "tok1" {
		t.Fatalf("device token changed by a rejected reclaim: %+v", got)
	}

	if rec := doAuth(t, h, "POST", "/register", body, token); rec.Code != http.StatusNoContent {
		t.Fatalf("owner refresh: got %d, want 204", rec.Code)
	}
	if got, _, _ := srv.store.lookup("geoff"); got.FCMToken != "tok2" {
		t.Fatalf("owner refresh did not update token: %+v", got)
	}
}

func TestSendHandler(t *testing.T) {
	srv, fp := newTestServer(t)
	h := srv.routes()
	sender := registerUser(t, h, "geoff", "geoff-tok")
	registerUser(t, h, "wife", "wife-tok")

	rec := doAuth(t, h, "POST", "/send", `{"toUserId":"wife","ciphertext":"SGVsbG8="}`, sender)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send to known: got %d, want 202", rec.Code)
	}
	if len(fp.calls) != 1 || fp.calls[0].token != "wife-tok" || fp.calls[0].ciphertext != "SGVsbG8=" {
		t.Fatalf("push not called correctly: %+v", fp.calls)
	}

	if rec := doAuth(t, h, "POST", "/send", `{"toUserId":"ghost","ciphertext":"x"}`, sender); rec.Code != http.StatusNotFound {
		t.Fatalf("send to unknown: got %d, want 404", rec.Code)
	}
	if rec := doAuth(t, h, "POST", "/send", `{"toUserId":"wife","ciphertext":""}`, sender); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing ciphertext: got %d, want 400", rec.Code)
	}
}

func TestSendHandlerRequiresRegistration(t *testing.T) {
	srv, fp := newTestServer(t)
	h := srv.routes()
	registerUser(t, h, "wife", "wife-tok")

	body := `{"toUserId":"wife","ciphertext":"SGVsbG8="}`
	if rec := do(t, h, "POST", "/send", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("send without token: got %d, want 401", rec.Code)
	}
	if rec := doAuth(t, h, "POST", "/send", body, "not-a-real-token"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("send with unknown token: got %d, want 401", rec.Code)
	}
	if len(fp.calls) != 0 {
		t.Fatalf("unauthenticated send reached FCM: %+v", fp.calls)
	}
}

func TestSendHandlerPushError(t *testing.T) {
	srv, fp := newTestServer(t)
	h := srv.routes()
	sender := registerUser(t, h, "geoff", "geoff-tok")
	registerUser(t, h, "wife", "wife-tok")
	fp.err = errors.New("fcm unavailable")

	if rec := doAuth(t, h, "POST", "/send", `{"toUserId":"wife","ciphertext":"x"}`, sender); rec.Code != http.StatusBadGateway {
		t.Fatalf("push error: got %d, want 502", rec.Code)
	}
}

func TestHealthHandler(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv.routes(), "GET", "/healthz", ``)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz: got %d %q, want 200 \"ok\"", rec.Code, rec.Body.String())
	}
}
