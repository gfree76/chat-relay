// Command chat-relay is a minimal, zero-knowledge push relay for the E2E chat app.
//
// It does two things:
//   - POST /register  {userId, fcmToken}      claim a userId, returning the auth token that owns it
//   - POST /send      {toUserId, ciphertext}  forward ciphertext to that user via FCM
//
// Both re-registering a userId and sending require that auth token as a bearer
// credential, so a userId can only be reclaimed and used by the client holding it.
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
	storePath := flag.String("store", envOr("STORE_PATH", "devices.db"), "path to the SQLite database")
	flag.Parse()

	st, err := openStore(*storePath)
	if err != nil {
		log.Fatalf("open store %s: %v", *storePath, err)
	}
	defer st.close()

	srv := newServer(st, fcmPush)

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
