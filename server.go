package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
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

type registerResponse struct {
	AuthToken string `json:"authToken"`
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

	token, err := srv.store.register(req.UserID, req.FCMToken)
	switch {
	case err == nil:
		log.Printf("registered %s", req.UserID)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(registerResponse{AuthToken: token}); err != nil {
			log.Printf("write register response for %s: %v", req.UserID, err)
		}
		return
	case !errors.Is(err, errUserTaken):
		log.Printf("register %s: %v", req.UserID, err)
		http.Error(w, "failed to register", http.StatusInternalServerError)
		return
	}

	owner, ok, err := srv.store.userByAuth(bearerToken(r))
	if err != nil {
		log.Printf("auth lookup: %v", err)
		http.Error(w, "failed to register", http.StatusInternalServerError)
		return
	}
	if !ok || owner != req.UserID {
		http.Error(w, "userId belongs to another client", http.StatusForbidden)
		return
	}
	if err := srv.store.refresh(req.UserID, req.FCMToken); err != nil {
		log.Printf("refresh %s: %v", req.UserID, err)
		http.Error(w, "failed to register", http.StatusInternalServerError)
		return
	}
	log.Printf("refreshed %s", req.UserID)
	w.WriteHeader(http.StatusNoContent)
}

func (srv *server) handleSend(w http.ResponseWriter, r *http.Request) {
	sender, ok, err := srv.store.userByAuth(bearerToken(r))
	if err != nil {
		log.Printf("auth lookup: %v", err)
		http.Error(w, "failed to deliver", http.StatusInternalServerError)
		return
	}
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "registration required", http.StatusUnauthorized)
		return
	}

	var req sendRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ToUserID == "" || req.Ciphertext == "" {
		http.Error(w, "toUserId and ciphertext are required", http.StatusBadRequest)
		return
	}
	dev, found, err := srv.store.lookup(req.ToUserID)
	if err != nil {
		log.Printf("lookup %s: %v", req.ToUserID, err)
		http.Error(w, "failed to deliver", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "recipient not registered", http.StatusNotFound)
		return
	}
	if err := srv.push(dev.FCMToken, req.Ciphertext); err != nil {
		log.Printf("push %s -> %s failed: %v", sender, req.ToUserID, err)
		http.Error(w, "failed to deliver", http.StatusBadGateway)
		return
	}
	log.Printf("pushed %s -> %s", sender, req.ToUserID)
	w.WriteHeader(http.StatusAccepted)
}

func (srv *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return h[len(prefix):]
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
