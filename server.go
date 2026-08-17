package main

import (
	"encoding/json"
	"log"
	"net/http"
)

// pushFunc delivers ciphertext to a device token. It's a field on server so
// tests can inject a fake and never touch real FCM.
type pushFunc func(fcmToken, ciphertext string) error

type server struct {
	store *store
	push  pushFunc
}

func newServer(s *store, push pushFunc) *server {
	return &server{store: s, push: push}
}

// routes returns the HTTP handler. Method-based patterns (Go 1.22) give us
// automatic 405s for the wrong verb.
func (srv *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /register", srv.handleRegister)
	mux.HandleFunc("POST /send", srv.handleSend)
	mux.HandleFunc("GET /healthz", srv.handleHealth)
	return mux
}

type registerRequest struct {
	UserID   string `json:"userId"`
	FCMToken string `json:"fcmToken"`
}

type sendRequest struct {
	ToUserID   string `json:"toUserId"`
	Ciphertext string `json:"ciphertext"`
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
	if err := srv.push(dev.FCMToken, req.Ciphertext); err != nil {
		log.Printf("push to %s failed: %v", req.ToUserID, err)
		http.Error(w, "failed to deliver", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (srv *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

// decodeJSON decodes the request body into dst, writing a 400 on failure.
// Returns true on success.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
