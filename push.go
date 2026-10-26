package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	fcmBaseURL = "https://fcm.googleapis.com"
	fcmScope   = "https://www.googleapis.com/auth/firebase.messaging"

	// maxDataBytes is FCM's payload limit, counted here as the sum of data keys
	// and values.
	maxDataBytes = 4096
)

// pusher delivers ciphertext from sender to the device identified by fid.
type pusher interface {
	push(ctx context.Context, fid, sender, ciphertext string) error
}

// errUnregistered means FCM reports the target FID will never be valid again.
var errUnregistered = errors.New("fcm: target is unregistered")

// fitsFCMPayload reports whether the data message for (sender, ciphertext) is
// within FCM's size limit. FCM's 400 INVALID_ARGUMENT means a dead target only
// when the payload is valid, so /send checks this before pushing.
func fitsFCMPayload(sender, ciphertext string) bool {
	return len("sender")+len(sender)+len("body")+len(ciphertext) <= maxDataBytes
}

// fcmSender sends data messages through the FCM HTTP v1 API.
type fcmSender struct {
	client   *http.Client
	endpoint string
}

type fcmConfig struct {
	BaseURL         string
	ProjectID       string
	CredentialsPath string        // service-account key JSON
	Timeout         time.Duration // per HTTP request, token mints included
}

// newFCMSender builds a sender from a service-account key file. It mints one
// access token up front so a missing or broken key fails at startup, not on
// the first send.
func newFCMSender(cfg fcmConfig) (*fcmSender, error) {
	if cfg.ProjectID == "" {
		return nil, errors.New("FCM_PROJECT_ID is not set")
	}
	if cfg.CredentialsPath == "" {
		return nil, errors.New("GOOGLE_APPLICATION_CREDENTIALS is not set; it must name the FCM service-account key file")
	}
	key, err := os.ReadFile(cfg.CredentialsPath)
	if err != nil {
		return nil, fmt.Errorf("read GOOGLE_APPLICATION_CREDENTIALS: %w", err)
	}
	// The token source keeps this context for every later mint, so it carries
	// the timeout client rather than a deadline.
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Timeout: cfg.Timeout})
	creds, err := google.CredentialsFromJSONWithType(ctx, key, google.ServiceAccount, fcmScope)
	if err != nil {
		return nil, fmt.Errorf("parse service-account key %s: %w", cfg.CredentialsPath, err)
	}
	if _, err := creds.TokenSource.Token(); err != nil {
		return nil, fmt.Errorf("mint FCM access token: %w", err)
	}
	client := oauth2.NewClient(ctx, creds.TokenSource)
	client.Timeout = cfg.Timeout
	return &fcmSender{
		client:   client,
		endpoint: cfg.BaseURL + "/v1/projects/" + url.PathEscape(cfg.ProjectID) + "/messages:send",
	}, nil
}

type fcmRequest struct {
	Message fcmMessage `json:"message"`
}

type fcmMessage struct {
	FID     string            `json:"fid"`
	Data    map[string]string `json:"data"`
	Android fcmAndroid        `json:"android"`
}

type fcmAndroid struct {
	Priority string `json:"priority"`
}

type fcmErrorResponse struct {
	Error struct {
		Status  string `json:"status"`
		Details []struct {
			ErrorCode string `json:"errorCode"`
		} `json:"details"`
	} `json:"error"`
}

func (f *fcmSender) push(ctx context.Context, fid, sender, ciphertext string) error {
	body, err := json.Marshal(fcmRequest{Message: fcmMessage{
		FID:     fid,
		Data:    map[string]string{"sender": sender, "body": ciphertext},
		Android: fcmAndroid{Priority: "high"},
	}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("fcm send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		// FCM has accepted the message; a broken body doesn't undo that.
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			log.Printf("fcm send: HTTP %d, reading response body: %v", resp.StatusCode, err)
		}
		return nil
	}

	code := fcmErrorCode(resp.Body)
	switch {
	case resp.StatusCode == http.StatusNotFound && code == "UNREGISTERED",
		resp.StatusCode == http.StatusBadRequest && code == "INVALID_ARGUMENT":
		return fmt.Errorf("%w (HTTP %d %s)", errUnregistered, resp.StatusCode, code)
	default:
		return fmt.Errorf("fcm send: HTTP %d %s", resp.StatusCode, code)
	}
}

// fcmErrorCode extracts the FcmError errorCode from an error response,
// falling back to the canonical status. The rest of the body is discarded
// because FCM may echo the request back in its message.
func fcmErrorCode(r io.Reader) string {
	var e fcmErrorResponse
	if err := json.NewDecoder(io.LimitReader(r, 64<<10)).Decode(&e); err != nil {
		return "(unparseable error body)"
	}
	for _, d := range e.Error.Details {
		if d.ErrorCode != "" {
			return d.ErrorCode
		}
	}
	return e.Error.Status
}
