// Command chat-relay is a minimal, zero-knowledge push relay for the E2E chat app.
//
// It does two things:
//   - POST /register  {userId, fcmToken}      remember where to push for a user
//   - POST /send      {toUserId, ciphertext}  forward ciphertext to that user via FCM
//
// It never sees plaintext or keys: messages are end-to-end encrypted on the
// clients, and public keys are exchanged out-of-band (QR). The relay is just a
// router that turns a userId into an FCM push.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"sync"
)

// Device is where to reach a user: their current FCM registration token.
type Device struct {
	FCMToken string
}

// store maps userId -> Device. In-memory for now; swap for SQLite/Bolt later.
type store struct {
	mu      sync.RWMutex
	devices map[string]Device
}

func newStore() *store {
	return &store{devices: make(map[string]Device)}
}

func (s *store) register(userID string, d Device) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices[userID] = d
}

func (s *store) lookup(userID string) (Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[userID]
	return d, ok
}

type registerRequest struct {
	UserID   string `json:"userId"`
	FCMToken string `json:"fcmToken"`
}

type sendRequest struct {
	ToUserID   string `json:"toUserId"`
	Ciphertext string `json:"ciphertext"`
}

type server struct {
	store *store
}

func (srv *server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.UserID == "" || req.FCMToken == "" {
		http.Error(w, "userId and fcmToken are required", http.StatusBadRequest)
		return
	}
	srv.store.register(req.UserID, Device{FCMToken: req.FCMToken})
	log.Printf("registered %s", req.UserID)
	w.WriteHeader(http.StatusNoContent)
}

func (srv *server) handleSend(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ToUserID == "" || req.Ciphertext == "" {
		http.Error(w, "toUserId and ciphertext are required", http.StatusBadRequest)
		return
	}
	dev, ok := srv.store.lookup(req.ToUserID)
	if !ok {
		http.Error(w, "recipient not registered", http.StatusNotFound)
		return
	}
	if err := sendPush(dev.FCMToken, req.Ciphertext); err != nil {
		log.Printf("push to %s failed: %v", req.ToUserID, err)
		http.Error(w, "failed to deliver", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// sendPush delivers the ciphertext to a device via FCM as a data message.
//
// TODO: implement the FCM HTTP v1 call. It needs:
//   - the Firebase project ID
//   - a Google service-account credential (OAuth2 token, scope
//     https://www.googleapis.com/auth/firebase.messaging)
//   - POST https://fcm.googleapis.com/v1/projects/<projectID>/messages:send
//     with body {"message":{"token":<token>,"data":{"ciphertext":<ciphertext>}}}
//
// golang.org/x/oauth2/google mints the token (same service-account model as the
// Android CI). Kept as a stub so the skeleton builds on the standard library alone.
func sendPush(fcmToken, ciphertext string) error {
	log.Printf("[stub] would push %d bytes of ciphertext to token %.12s...", len(ciphertext), fcmToken)
	return nil
}

// decodeJSON decodes the request body into dst, writing a 400 on failure.
// Returns true on success. (Method enforcement is handled by the router.)
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func main() {
	addr := flag.String("addr", ":8080", "address to listen on")
	flag.Parse()

	srv := &server{store: newStore()}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /register", srv.handleRegister)
	mux.HandleFunc("POST /send", srv.handleSend)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	log.Printf("chat-relay listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
