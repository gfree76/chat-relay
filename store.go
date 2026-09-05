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

// Device is where to reach a user: their current FCM registration token.
type Device struct {
	FCMToken string
}

// errUserTaken means the userId is already claimed by another client.
var errUserTaken = errors.New("user already registered")

// store maps userId -> Device, backed by SQLite.
type store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS devices (
	user_id       TEXT PRIMARY KEY,
	fcm_token     TEXT NOT NULL,
	auth_hash     BLOB NOT NULL UNIQUE,
	registered_at INTEGER NOT NULL
);`

// openStore opens the database at path, creating the file and schema if needed.
func openStore(path string) (*store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &store{db: db}, nil
}

func (s *store) close() error { return s.db.Close() }

// register claims userID and returns the auth token proving ownership of it.
// The token is returned once here; only its hash is stored.
func (s *store) register(userID, fcmToken string) (string, error) {
	token, hash, err := newAuthToken()
	if err != nil {
		return "", err
	}
	res, err := s.db.Exec(
		`INSERT INTO devices (user_id, fcm_token, auth_hash, registered_at)
		 VALUES (?, ?, ?, ?) ON CONFLICT(user_id) DO NOTHING`,
		userID, fcmToken, hash, time.Now().Unix())
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

// refresh points an existing userID at a new device token.
func (s *store) refresh(userID, fcmToken string) error {
	_, err := s.db.Exec(
		`UPDATE devices SET fcm_token = ?, registered_at = ? WHERE user_id = ?`,
		fcmToken, time.Now().Unix(), userID)
	return err
}

func (s *store) lookup(userID string) (Device, bool, error) {
	var d Device
	err := s.db.QueryRow(`SELECT fcm_token FROM devices WHERE user_id = ?`, userID).Scan(&d.FCMToken)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, false, nil
	}
	if err != nil {
		return Device{}, false, err
	}
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
