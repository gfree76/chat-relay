package main

import "sync"

// Device is where to reach a user: their current FCM registration token.
type Device struct {
	FCMToken string
}

// store maps userId -> Device. In-memory for now; swap for SQLite/Bolt later.
type store struct {
	mu      sync.RWMutex
	devices map[string]Device
}

func newStore() *store {
	return &store{devices: make(map[string]Device)}
}

func (s *store) register(userID string, d Device) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices[userID] = d
}

func (s *store) lookup(userID string) (Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[userID]
	return d, ok
}
