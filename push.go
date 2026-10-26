package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// errUnregistered means FCM reports the target FID will never be valid again,
// so its registration should be deleted.
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

// newFCMSender builds a sender for projectID using Application Default
// Credentials (GOOGLE_APPLICATION_CREDENTIALS). It fetches one access token up
// front so a missing or broken key fails at startup, not on the first send.
func newFCMSender(ctx context.Context, baseURL, projectID string) (*fcmSender, error) {
	if projectID == "" {
		return nil, errors.New("FCM_PROJECT_ID is not set")
	}
	creds, err := google.FindDefaultCredentials(ctx, fcmScope)
	if err != nil {
		return nil, fmt.Errorf("find FCM credentials (set GOOGLE_APPLICATION_CREDENTIALS): %w", err)
	}
	if _, err := creds.TokenSource.Token(); err != nil {
		return nil, fmt.Errorf("fetch FCM access token: %w", err)
	}
	client := oauth2.NewClient(ctx, creds.TokenSource)
	client.Timeout = 10 * time.Second
	return &fcmSender{
		client:   client,
		endpoint: baseURL + "/v1/projects/" + url.PathEscape(projectID) + "/messages:send",
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
		_, _ = io.Copy(io.Discard, resp.Body)
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
