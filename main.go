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
	"flag"
	"log"
	"net/http"
	"os"
)

func main() {
	addr := flag.String("addr", envOr("ADDR", ":8080"), "address to listen on")
	flag.Parse()

	srv := newServer(newStore(), fcmPush)

	log.Printf("chat-relay listening on %s", *addr)
	if err := http.ListenAndServe(*addr, srv.routes()); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
