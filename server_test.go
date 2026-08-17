package main

import (
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

func newTestServer() (*server, *fakePusher) {
	fp := &fakePusher{}
	return newServer(newStore(), fp.push), fp
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRegisterHandler(t *testing.T) {
	srv, _ := newTestServer()
	h := srv.routes()

	if rec := do(t, h, "POST", "/register", `{"userId":"geoff","fcmToken":"tok"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("valid register: got %d, want 204", rec.Code)
	}
	if _, ok := srv.store.lookup("geoff"); !ok {
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

func TestSendHandler(t *testing.T) {
	srv, fp := newTestServer()
	h := srv.routes()
	srv.store.register("wife", Device{FCMToken: "wife-tok"})

	rec := do(t, h, "POST", "/send", `{"toUserId":"wife","ciphertext":"SGVsbG8="}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send to known: got %d, want 202", rec.Code)
	}
	if len(fp.calls) != 1 || fp.calls[0].token != "wife-tok" || fp.calls[0].ciphertext != "SGVsbG8=" {
		t.Fatalf("push not called correctly: %+v", fp.calls)
	}

	if rec := do(t, h, "POST", "/send", `{"toUserId":"ghost","ciphertext":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("send to unknown: got %d, want 404", rec.Code)
	}
	if rec := do(t, h, "POST", "/send", `{"toUserId":"wife","ciphertext":""}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing ciphertext: got %d, want 400", rec.Code)
	}
}

func TestSendHandlerPushError(t *testing.T) {
	srv, fp := newTestServer()
	fp.err = errors.New("fcm unavailable")
	h := srv.routes()
	srv.store.register("wife", Device{FCMToken: "wife-tok"})

	if rec := do(t, h, "POST", "/send", `{"toUserId":"wife","ciphertext":"x"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("push error: got %d, want 502", rec.Code)
	}
}

func TestHealthHandler(t *testing.T) {
	srv, _ := newTestServer()
	rec := do(t, srv.routes(), "GET", "/healthz", ``)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz: got %d %q, want 200 \"ok\"", rec.Code, rec.Body.String())
	}
}
