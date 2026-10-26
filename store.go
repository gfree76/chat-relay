package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Device is where to reach a user: their Firebase Installation ID (FID) and
// when it was last registered.
type Device struct {
	FID          string
	RegisteredAt time.Time
}

// errUserTaken means the userId is already claimed by another client.
var errUserTaken = errors.New("user already registered")

// store maps userId -> Device, backed by SQLite.
type store struct {
	db  *sql.DB
	now func() time.Time
}

// migrations[i] moves the schema from PRAGMA user_version i to i+1. Append
// only: deployed databases have already applied the earlier entries.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS devices (
		user_id       TEXT PRIMARY KEY,
		fcm_token     TEXT NOT NULL,
		auth_hash     BLOB NOT NULL UNIQUE,
		registered_at INTEGER NOT NULL
	)`,
	`ALTER TABLE devices RENAME COLUMN fcm_token TO fid`,
}

// openStore opens the database at path, creating the file and migrating the
// schema to the current version if needed.
func openStore(path string) (*store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return &store{db: db, now: time.Now}, nil
}

func migrate(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var version int
	if err := tx.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("database is at schema version %d, newer than this binary's %d", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		if _, err := tx.Exec(migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations))); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *store) close() error { return s.db.Close() }

// register claims userID and returns the auth token proving ownership of it.
// The token is returned once here; only its hash is stored.
func (s *store) register(userID, fid string) (string, error) {
	token, hash, err := newAuthToken()
	if err != nil {
		return "", err
	}
	res, err := s.db.Exec(
		`INSERT INTO devices (user_id, fid, auth_hash, registered_at)
		 VALUES (?, ?, ?, ?) ON CONFLICT(user_id) DO NOTHING`,
		userID, fid, hash, s.now().Unix())
	if err != nil {
		return "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", errUserTaken
	}
	return token, nil
}

// refresh points an existing userID at a (possibly unchanged) FID and stamps
// the registration time.
func (s *store) refresh(userID, fid string) error {
	_, err := s.db.Exec(
		`UPDATE devices SET fid = ?, registered_at = ? WHERE user_id = ?`,
		fid, s.now().Unix(), userID)
	return err
}

// unregister deletes userID's registration, but only while it still points at
// fid, so a registration refreshed since the failed send survives.
func (s *store) unregister(userID, fid string) error {
	_, err := s.db.Exec(`DELETE FROM devices WHERE user_id = ? AND fid = ?`, userID, fid)
	return err
}

func (s *store) lookup(userID string) (Device, bool, error) {
	var (
		d          Device
		registered int64
	)
	err := s.db.QueryRow(`SELECT fid, registered_at FROM devices WHERE user_id = ?`, userID).Scan(&d.FID, &registered)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, false, nil
	}
	if err != nil {
		return Device{}, false, err
	}
	d.RegisteredAt = time.Unix(registered, 0)
	return d, true, nil
}

// userByAuth resolves an auth token to the userId that owns it.
func (s *store) userByAuth(token string) (string, bool, error) {
	if token == "" {
		return "", false, nil
	}
	sum := sha256.Sum256([]byte(token))
	var userID string
	err := s.db.QueryRow(`SELECT user_id FROM devices WHERE auth_hash = ?`, sum[:]).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return userID, true, nil
}

func newAuthToken() (token string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}
