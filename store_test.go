package main

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
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
	if err != nil || !ok || got.FID != "tok1" {
		t.Fatalf("lookup = %+v ok:%v err:%v, want {tok1} true nil", got, ok, err)
	}

	if err := s.refresh("geoff", "tok2"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got, _, _ := s.lookup("geoff"); got.FID != "tok2" {
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
	if got, _, _ := s.lookup("geoff"); got.FID != "tok1" {
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
	if err != nil || !ok || got.FID != "tok1" {
		t.Fatalf("after reopen = %+v ok:%v err:%v, want {tok1} true nil", got, ok, err)
	}
	if user, ok, _ := second.userByAuth(token); !ok || user != "geoff" {
		t.Fatalf("auth token did not survive reopen: %q ok:%v", user, ok)
	}
}

func TestStoreRefreshStampsRegisteredAt(t *testing.T) {
	s := testStore(t)
	clock := time.Unix(1_000, 0)
	s.now = func() time.Time { return clock }

	if _, err := s.register("geoff", "fid1"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if got, _, _ := s.lookup("geoff"); !got.RegisteredAt.Equal(clock) {
		t.Fatalf("registered_at after register = %v, want %v", got.RegisteredAt, clock)
	}

	clock = time.Unix(2_000, 0)
	if err := s.refresh("geoff", "fid1"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got, _, _ := s.lookup("geoff"); !got.RegisteredAt.Equal(clock) {
		t.Fatalf("registered_at after same-FID refresh = %v, want %v", got.RegisteredAt, clock)
	}
}

func TestStoreUnregister(t *testing.T) {
	s := testStore(t)
	if _, err := s.register("geoff", "fid1"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := s.unregister("geoff", "stale-fid"); err != nil {
		t.Fatalf("unregister stale: %v", err)
	}
	if _, ok, _ := s.lookup("geoff"); !ok {
		t.Fatal("unregister of a stale FID deleted the current registration")
	}

	if err := s.unregister("geoff", "fid1"); err != nil {
		t.Fatalf("unregister: %v", err)
	}
	if _, ok, _ := s.lookup("geoff"); ok {
		t.Fatal("registration survived unregister")
	}
	if _, err := s.register("geoff", "fid2"); err != nil {
		t.Fatalf("re-register after unregister: %v", err)
	}
}

// TestStoreMigratesPR8Database opens a database written with the original
// schema (user_version 0, fcm_token column) and checks rows and auth survive.
func TestStoreMigratesPR8Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.db")
	token, hash, err := newAuthToken()
	if err != nil {
		t.Fatalf("newAuthToken: %v", err)
	}

	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open old db: %v", err)
	}
	if _, err := old.Exec(`CREATE TABLE devices (
		user_id       TEXT PRIMARY KEY,
		fcm_token     TEXT NOT NULL,
		auth_hash     BLOB NOT NULL UNIQUE,
		registered_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	if _, err := old.Exec(`INSERT INTO devices VALUES ('geoff', 'old-fid', ?, 1000)`, hash); err != nil {
		t.Fatalf("insert old row: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close old db: %v", err)
	}

	for range 2 { // second open must be a no-op
		s, err := openStore(path)
		if err != nil {
			t.Fatalf("openStore on old db: %v", err)
		}
		got, ok, err := s.lookup("geoff")
		if err != nil || !ok || got.FID != "old-fid" || !got.RegisteredAt.Equal(time.Unix(1000, 0)) {
			t.Fatalf("lookup after migration = %+v ok:%v err:%v, want {old-fid 1000} true nil", got, ok, err)
		}
		if user, ok, _ := s.userByAuth(token); !ok || user != "geoff" {
			t.Fatalf("auth token lost in migration: %q ok:%v", user, ok)
		}
		var version int
		if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != len(migrations) {
			t.Fatalf("user_version = %d err:%v, want %d", version, err, len(migrations))
		}
		s.close()
	}
}

func TestStoreRejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	db.Close()

	if s, err := openStore(path); err == nil {
		s.close()
		t.Fatal("openStore accepted a database from a newer binary")
	}
}
