package main

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// testStore opens a store on a throwaway database file.
func testStore(t *testing.T) *store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "devices.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { s.close() })
	return s
}

func TestStoreRegisterLookup(t *testing.T) {
	s := testStore(t)

	if _, ok, err := s.lookup("nobody"); err != nil || ok {
		t.Fatalf("lookup unknown = ok:%v err:%v, want false nil", ok, err)
	}

	token, err := s.register("geoff", "tok1")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if token == "" {
		t.Fatal("register returned an empty auth token")
	}

	got, ok, err := s.lookup("geoff")
	if err != nil || !ok || got.FCMToken != "tok1" {
		t.Fatalf("lookup = %+v ok:%v err:%v, want {tok1} true nil", got, ok, err)
	}

	if err := s.refresh("geoff", "tok2"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got, _, _ := s.lookup("geoff"); got.FCMToken != "tok2" {
		t.Fatalf("after refresh = %+v, want {tok2}", got)
	}
}

func TestStoreRegisterRejectsTakenUser(t *testing.T) {
	s := testStore(t)

	if _, err := s.register("geoff", "tok1"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, err := s.register("geoff", "attacker-tok"); !errors.Is(err, errUserTaken) {
		t.Fatalf("second register err = %v, want errUserTaken", err)
	}
	if got, _, _ := s.lookup("geoff"); got.FCMToken != "tok1" {
		t.Fatalf("device token overwritten by rejected register: %+v", got)
	}
}

func TestStoreConcurrentRegisterOneWinner(t *testing.T) {
	s := testStore(t)

	const contenders = 2
	tokens := make([]string, contenders)
	errs := make([]error, contenders)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tokens[i], errs[i] = s.register("geoff", "tok")
		}()
	}
	close(start)
	wg.Wait()

	var winners, taken int
	for i := range contenders {
		switch {
		case errs[i] == nil && tokens[i] != "":
			winners++
			if user, ok, err := s.userByAuth(tokens[i]); err != nil || !ok || user != "geoff" {
				t.Fatalf("winner token resolves to %q ok:%v err:%v, want geoff true nil", user, ok, err)
			}
		case errors.Is(errs[i], errUserTaken):
			taken++
		default:
			t.Fatalf("contender %d: token %q err %v, want a token or errUserTaken", i, tokens[i], errs[i])
		}
	}
	if winners != 1 || taken != contenders-1 {
		t.Fatalf("winners=%d taken=%d, want 1 and %d", winners, taken, contenders-1)
	}
}

func TestStoreUserByAuth(t *testing.T) {
	s := testStore(t)

	token, err := s.register("geoff", "tok1")
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	user, ok, err := s.userByAuth(token)
	if err != nil || !ok || user != "geoff" {
		t.Fatalf("userByAuth = %q ok:%v err:%v, want geoff true nil", user, ok, err)
	}
	if _, ok, _ := s.userByAuth("wrong-token"); ok {
		t.Fatal("userByAuth accepted an unknown token")
	}
	if _, ok, _ := s.userByAuth(""); ok {
		t.Fatal("userByAuth accepted an empty token")
	}
}

func TestStoreSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.db")

	first, err := openStore(path)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	token, err := first.register("geoff", "tok1")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := first.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := openStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.close()

	got, ok, err := second.lookup("geoff")
	if err != nil || !ok || got.FCMToken != "tok1" {
		t.Fatalf("after reopen = %+v ok:%v err:%v, want {tok1} true nil", got, ok, err)
	}
	if user, ok, _ := second.userByAuth(token); !ok || user != "geoff" {
		t.Fatalf("auth token did not survive reopen: %q ok:%v", user, ok)
	}
}
