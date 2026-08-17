package main

import "log"

// fcmPush delivers ciphertext to a device via FCM as a data message.
//
// TODO: implement the FCM HTTP v1 call. It needs:
//   - the Firebase project ID (env FCM_PROJECT_ID)
//   - a Google service-account OAuth2 token, scope
//     https://www.googleapis.com/auth/firebase.messaging
//     (golang.org/x/oauth2/google, from GOOGLE_APPLICATION_CREDENTIALS)
//   - POST https://fcm.googleapis.com/v1/projects/<projectID>/messages:send
//     body: {"message":{"token":<token>,"data":{"ciphertext":<ciphertext>}}}
//
// Kept as a stub (logs instead of sending) so the skeleton builds and tests run
// on the standard library alone. The relay is otherwise fully wired.
func fcmPush(fcmToken, ciphertext string) error {
	log.Printf("[stub] would push %d bytes of ciphertext to token %.12s...", len(ciphertext), fcmToken)
	return nil
}
