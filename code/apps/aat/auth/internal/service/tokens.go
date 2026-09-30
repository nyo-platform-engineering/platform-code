package service

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

var (
	ErrCredential = errors.New("invalid credentials or token")
	ErrCapacity   = errors.New("session capacity reached")
)

type Pair struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
}

type session struct {
	client            string
	access, refresh   [32]byte
	expires, deadline time.Time
	history           [][32]byte
}

type Store struct {
	mu                    sync.Mutex
	clients               map[string][32]byte
	access, refresh       map[[32]byte]*session
	sessions              map[*session]bool
	accessTTL, refreshTTL time.Duration
	capacity              int
	now                   func() time.Time
}

func NewStore(clients map[string]string, accessTTL, refreshTTL time.Duration, capacity int) *Store {
	if accessTTL < time.Second || refreshTTL < accessTTL || capacity < 1 {
		panic("invalid token lifetimes or session capacity")
	}
	s := &Store{clients: map[string][32]byte{}, access: map[[32]byte]*session{}, refresh: map[[32]byte]*session{}, sessions: map[*session]bool{}, accessTTL: accessTTL, refreshTTL: refreshTTL, capacity: capacity, now: time.Now}
	for id, secret := range clients {
		if secret == "" {
			panic("empty client secret")
		}
		s.clients[id] = sha256.Sum256([]byte(secret))
	}
	return s
}

func token() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func (s *Store) remove(session *session) {
	delete(s.access, session.access)
	for _, hash := range session.history {
		delete(s.refresh, hash)
	}
	delete(s.sessions, session)
}

func (s *Store) prune(now time.Time) {
	for session := range s.sessions {
		if !now.Before(session.deadline) {
			s.remove(session)
		}
	}
}

// issue is called under the lock. Keeping spent refresh hashes detects replay.
func (s *Store) issue(session *session, now time.Time) Pair {
	delete(s.access, session.access)
	access, refresh := token(), token()
	session.access = sha256.Sum256([]byte(access))
	session.refresh = sha256.Sum256([]byte(refresh))
	session.expires = now.Add(s.accessTTL)
	if session.expires.After(session.deadline) {
		session.expires = session.deadline
	}
	session.history = append(session.history, session.refresh)
	s.access[session.access] = session
	s.refresh[session.refresh] = session
	return Pair{access, "Bearer", int64(session.expires.Sub(now) / time.Second), refresh, int64(session.deadline.Sub(now) / time.Second)}
}

func (s *Store) Login(id, secret string) (Pair, error) {
	want, exists := s.clients[id]
	got := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 || !exists {
		return Pair{}, ErrCredential
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.prune(now)
	if len(s.sessions) >= s.capacity {
		return Pair{}, ErrCapacity
	}
	session := &session{client: id, deadline: now.Add(s.refreshTTL)}
	s.sessions[session] = true
	return s.issue(session, now), nil
}

func (s *Store) Refresh(value string) (Pair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.prune(now)
	hash := sha256.Sum256([]byte(value))
	session := s.refresh[hash]
	if session == nil {
		return Pair{}, ErrCredential
	}
	// Cap rotation history as well as session count to bound memory use.
	if hash != session.refresh || len(session.history) >= 128 {
		s.remove(session)
		return Pair{}, ErrCredential
	}
	return s.issue(session, now), nil
}

func (s *Store) Identity(value string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.prune(now)
	session := s.access[sha256.Sum256([]byte(value))]
	if session == nil || !now.Before(session.expires) {
		return "", false
	}
	return session.client, true
}
