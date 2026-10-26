package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type pushCall struct{ fid, sender, ciphertext string }

func (f *fakePusher) push(_ context.Context, fid, sender, ciphertext string) error {
	f.calls = append(f.calls, pushCall{fid, sender, ciphertext})
	return f.err
}

func newTestServer(t *testing.T) (*server, *fakePusher) {
	t.Helper()
	fp := &fakePusher{}
	return newServer(testStore(t), fp), fp
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
func registerUser(t *testing.T, h http.Handler, userID, fid string) string {
	t.Helper()
	rec := do(t, h, "POST", "/register", `{"userId":"`+userID+`","fid":"`+fid+`"}`)
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
	if rec := do(t, h, "POST", "/register", `{"userId":"","fid":""}`); rec.Code != http.StatusBadRequest {
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

	body := `{"userId":"geoff","fid":"tok2"}`
	if rec := do(t, h, "POST", "/register", body); rec.Code != http.StatusForbidden {
		t.Fatalf("reclaim without token: got %d, want 403", rec.Code)
	}
	if rec := doAuth(t, h, "POST", "/register", body, "wrong-token"); rec.Code != http.StatusForbidden {
		t.Fatalf("reclaim with wrong token: got %d, want 403", rec.Code)
	}
	if got, _, _ := srv.store.lookup("geoff"); got.FID != "tok1" {
		t.Fatalf("device token changed by a rejected reclaim: %+v", got)
	}

	if rec := doAuth(t, h, "POST", "/register", body, token); rec.Code != http.StatusNoContent {
		t.Fatalf("owner refresh: got %d, want 204", rec.Code)
	}
	if got, _, _ := srv.store.lookup("geoff"); got.FID != "tok2" {
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
	if want := (pushCall{"wife-tok", "geoff", "SGVsbG8="}); len(fp.calls) != 1 || fp.calls[0] != want {
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

func TestSendHandlerDeadFIDMarksRecipientUnreachable(t *testing.T) {
	srv, fp := newTestServer(t)
	h := srv.routes()
	sender := registerUser(t, h, "geoff", "geoff-tok")
	wife := registerUser(t, h, "wife", "wife-tok")
	fp.err = fmt.Errorf("%w (HTTP 404 UNREGISTERED)", errUnregistered)

	send := `{"toUserId":"wife","ciphertext":"x"}`
	if rec := doAuth(t, h, "POST", "/send", send, sender); rec.Code != http.StatusGone {
		t.Fatalf("dead FID: got %d, want 410", rec.Code)
	}
	if got, ok, _ := srv.store.lookup("wife"); !ok || got.FID != "" {
		t.Fatalf("recipient after dead FID = %+v ok:%v, want registered with no FID", got, ok)
	}

	fp.err = nil
	if rec := doAuth(t, h, "POST", "/send", send, sender); rec.Code != http.StatusGone {
		t.Fatalf("send to unreachable: got %d, want 410", rec.Code)
	}
	if len(fp.calls) != 1 {
		t.Fatalf("send to unreachable recipient reached FCM: %d calls", len(fp.calls))
	}

	if rec := do(t, h, "POST", "/register", `{"userId":"wife","fid":"attacker-fid"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("stranger claiming unreachable user: got %d, want 403", rec.Code)
	}
	if rec := doAuth(t, h, "POST", "/register", `{"userId":"wife","fid":"wife-fid2"}`, wife); rec.Code != http.StatusNoContent {
		t.Fatalf("owner re-register: got %d, want 204", rec.Code)
	}
	if rec := doAuth(t, h, "POST", "/send", send, sender); rec.Code != http.StatusAccepted {
		t.Fatalf("send after owner re-register: got %d, want 202", rec.Code)
	}
	if last := fp.calls[len(fp.calls)-1]; last.fid != "wife-fid2" {
		t.Fatalf("push went to %q, want wife-fid2", last.fid)
	}
}

func TestSendHandlerDeadFIDPersistFailure(t *testing.T) {
	srv, fp := newTestServer(t)
	h := srv.routes()
	sender := registerUser(t, h, "geoff", "geoff-tok")
	registerUser(t, h, "wife", "wife-tok")
	fp.err = errUnregistered
	if _, err := srv.store.db.Exec(`CREATE TRIGGER fail_clear BEFORE UPDATE OF fid ON devices
		WHEN NEW.fid IS NULL BEGIN SELECT RAISE(ABORT, 'disk on fire'); END`); err != nil {
		t.Fatalf("install failing trigger: %v", err)
	}

	if rec := doAuth(t, h, "POST", "/send", `{"toUserId":"wife","ciphertext":"x"}`, sender); rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed unreachable mark: got %d, want 500", rec.Code)
	}
}

func TestSendHandlerRejectsOversizedPayload(t *testing.T) {
	srv, fp := newTestServer(t)
	h := srv.routes()
	sender := registerUser(t, h, "geoff", "geoff-tok")
	registerUser(t, h, "wife", "wife-tok")

	body := `{"toUserId":"wife","ciphertext":"` + strings.Repeat("A", maxDataBytes) + `"}`
	if rec := doAuth(t, h, "POST", "/send", body, sender); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized ciphertext: got %d, want 413", rec.Code)
	}
	if len(fp.calls) != 0 {
		t.Fatalf("oversized payload reached FCM: %d calls", len(fp.calls))
	}
}

func TestHealthHandler(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv.routes(), "GET", "/healthz", ``)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz: got %d %q, want 200 \"ok\"", rec.Code, rec.Body.String())
	}
}
