package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testAccessToken = "test-access-token"

func serveToken(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"access_token":"`+testAccessToken+`","token_type":"Bearer","expires_in":3600}`)
}

// writeServiceAccount writes a service-account key whose token_uri is served
// by tokenHandler and returns its path, so newFCMSender runs its real
// credential path offline.
func writeServiceAccount(t *testing.T, tokenHandler http.HandlerFunc) string {
	t.Helper()
	tokenSrv := httptest.NewServer(tokenHandler)
	t.Cleanup(tokenSrv.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	sa, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "org-test",
		"private_key_id": "test-key",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "relay@org-test.iam.gserviceaccount.com",
		"token_uri":      tokenSrv.URL,
	})
	if err != nil {
		t.Fatalf("marshal service account: %v", err)
	}
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, sa, 0o600); err != nil {
		t.Fatalf("write service account: %v", err)
	}
	return path
}

func testFCMConfig(baseURL, credentialsPath string) fcmConfig {
	return fcmConfig{BaseURL: baseURL, ProjectID: "org-test", CredentialsPath: credentialsPath, Timeout: 5 * time.Second}
}

// testFCM returns a real fcmSender whose FCM endpoint is served by h.
func testFCM(t *testing.T, h http.HandlerFunc) *fcmSender {
	t.Helper()
	fcmSrv := httptest.NewServer(h)
	t.Cleanup(fcmSrv.Close)
	f, err := newFCMSender(testFCMConfig(fcmSrv.URL, writeServiceAccount(t, serveToken)))
	if err != nil {
		t.Fatalf("newFCMSender: %v", err)
	}
	return f
}

func TestNewFCMSenderRejectsBadConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	valid := writeServiceAccount(t, serveToken)

	cases := []struct {
		name    string
		mutate  func(*fcmConfig)
		wantErr string
	}{
		{"no project", func(c *fcmConfig) { c.ProjectID = "" }, "FCM_PROJECT_ID is not set"},
		{"no credentials path", func(c *fcmConfig) { c.CredentialsPath = "" }, "GOOGLE_APPLICATION_CREDENTIALS is not set"},
		{"missing file", func(c *fcmConfig) { c.CredentialsPath = filepath.Join(dir, "missing.json") }, "read GOOGLE_APPLICATION_CREDENTIALS"},
		{"not JSON", func(c *fcmConfig) { c.CredentialsPath = write("garbage.json", "not json") }, "parse service-account key"},
		{"not a service account", func(c *fcmConfig) {
			c.CredentialsPath = write("user.json", `{"type":"authorized_user","client_id":"x","client_secret":"y","refresh_token":"z"}`)
		}, "parse service-account key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testFCMConfig(fcmBaseURL, valid)
			tc.mutate(&cfg)
			if _, err := newFCMSender(cfg); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestNewFCMSenderIgnoresAmbientCredentials(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", writeServiceAccount(t, serveToken))
	if _, err := newFCMSender(testFCMConfig(fcmBaseURL, "")); err == nil {
		t.Fatal("newFCMSender fell back to ambient credentials with no explicit key path")
	}
}

func TestNewFCMSenderFailsWhenTokenRejected(t *testing.T) {
	path := writeServiceAccount(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
	})
	if _, err := newFCMSender(testFCMConfig(fcmBaseURL, path)); err == nil || !strings.Contains(err.Error(), "mint FCM access token") {
		t.Fatalf("err = %v, want a token mint failure", err)
	}
}

func TestNewFCMSenderTokenMintTimesOut(t *testing.T) {
	release := make(chan struct{})
	path := writeServiceAccount(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)

	cfg := testFCMConfig(fcmBaseURL, path)
	cfg.Timeout = 100 * time.Millisecond
	start := time.Now()
	_, err := newFCMSender(cfg)
	if err == nil {
		t.Fatal("newFCMSender succeeded against a hung token server")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("startup took %v against a hung token server, want about %v", elapsed, cfg.Timeout)
	}
}

func TestFCMSenderSendsDataMessage(t *testing.T) {
	var (
		gotMethod, gotPath, gotAuth string
		gotBody                     map[string]any
	)
	f := testFCM(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode FCM request: %v", err)
		}
		_, _ = io.WriteString(w, `{"name":"projects/org-test/messages/1"}`)
	})

	if err := f.push(context.Background(), "the-fid", "geoff", "Q0lQSEVS"); err != nil {
		t.Fatalf("push: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/projects/org-test/messages:send" {
		t.Fatalf("request = %s %s, want POST /v1/projects/org-test/messages:send", gotMethod, gotPath)
	}
	if gotAuth != "Bearer "+testAccessToken {
		t.Fatalf("Authorization = %q, want the service-account access token", gotAuth)
	}
	var want map[string]any
	_ = json.Unmarshal([]byte(`{"message":{"fid":"the-fid","data":{"sender":"geoff","body":"Q0lQSEVS"},"android":{"priority":"high"}}}`), &want)
	if g, w := mustJSON(t, gotBody), mustJSON(t, want); g != w {
		t.Fatalf("body = %s\nwant   %s", g, w)
	}
}

func TestFCMSenderErrorMapping(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		body         string
		unregistered bool
	}{
		{"unregistered", 404, fcmError("NOT_FOUND", "UNREGISTERED"), true},
		{"invalid argument", 400, fcmError("INVALID_ARGUMENT", "INVALID_ARGUMENT"), true},
		{"not found without errorCode", 404, `{"error":{"code":404,"status":"NOT_FOUND"}}`, false},
		{"sender mismatch", 403, fcmError("PERMISSION_DENIED", "SENDER_ID_MISMATCH"), false},
		{"quota", 429, fcmError("RESOURCE_EXHAUSTED", "QUOTA_EXCEEDED"), false},
		{"unavailable", 503, `<html>upstream down</html>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := testFCM(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			err := f.push(context.Background(), "secret-fid", "geoff", "secret-ciphertext")
			if err == nil {
				t.Fatal("push succeeded, want an error")
			}
			if got := errors.Is(err, errUnregistered); got != tc.unregistered {
				t.Fatalf("errors.Is(%v, errUnregistered) = %v, want %v", err, got, tc.unregistered)
			}
			if msg := err.Error(); strings.Contains(msg, "secret") {
				t.Fatalf("error leaks the FID or ciphertext: %q", msg)
			}
		})
	}
}

func TestFCMSenderLogsBrokenSuccessBody(t *testing.T) {
	f := testFCM(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, `{"na`)
	})
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	if err := f.push(context.Background(), "the-fid", "geoff", "x"); err != nil {
		t.Fatalf("push after FCM accepted = %v, want nil", err)
	}
	if !strings.Contains(logs.String(), "reading response body") {
		t.Fatalf("truncated success body not logged; logs: %q", logs.String())
	}
}

func TestFitsFCMPayload(t *testing.T) {
	overhead := len("sender") + len("geoff") + len("body")
	if !fitsFCMPayload("geoff", strings.Repeat("A", maxDataBytes-overhead)) {
		t.Fatal("payload at the limit rejected")
	}
	if fitsFCMPayload("geoff", strings.Repeat("A", maxDataBytes-overhead+1)) {
		t.Fatal("payload over the limit accepted")
	}
}

func fcmError(status, errorCode string) string {
	return `{"error":{"code":0,"message":"echoes secret-fid","status":"` + status + `","details":[` +
		`{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"` + errorCode + `"}]}}`
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
