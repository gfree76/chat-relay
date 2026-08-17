package main

import "testing"

func TestStoreRegisterLookup(t *testing.T) {
	s := newStore()

	if _, ok := s.lookup("nobody"); ok {
		t.Fatal("expected miss for unknown user")
	}

	s.register("geoff", Device{FCMToken: "tok1"})
	got, ok := s.lookup("geoff")
	if !ok || got.FCMToken != "tok1" {
		t.Fatalf("lookup after register = %+v ok=%v, want {tok1} true", got, ok)
	}

	// re-registering overwrites (device token rotates)
	s.register("geoff", Device{FCMToken: "tok2"})
	if got, _ := s.lookup("geoff"); got.FCMToken != "tok2" {
		t.Fatalf("overwrite failed: got %+v, want {tok2}", got)
	}
}
