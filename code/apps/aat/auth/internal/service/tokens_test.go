package service

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func testStore() (*Store, *time.Time) {
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	s := NewStore(map[string]string{"public": "public-secret", "responder": "responder-secret", "analyst": "analyst-secret"}, time.Minute, time.Hour, 2)
	s.now = func() time.Time { return now }
	return s, &now
}

func TestIdentitiesExpiryRotationAndReplay(t *testing.T) {
	for _, id := range []string{"public", "responder", "analyst"} {
		t.Run(id, func(t *testing.T) {
			s, now := testStore()
			if _, err := s.Login(id, "wrong"); !errors.Is(err, ErrCredential) {
				t.Fatal(err)
			}
			pair, err := s.Login(id, id+"-secret")
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := s.Identity(pair.AccessToken); !ok || got != id {
				t.Fatal(got, ok)
			}
			if _, ok := s.Identity(pair.RefreshToken); ok {
				t.Fatal("refresh used as access")
			}
			*now = now.Add(time.Minute)
			if _, ok := s.Identity(pair.AccessToken); ok {
				t.Fatal("expired access accepted")
			}
			rotated, err := s.Refresh(pair.RefreshToken)
			if err != nil || rotated.AccessToken == pair.AccessToken || rotated.RefreshToken == pair.RefreshToken {
				t.Fatal("rotation", err)
			}
			if got, ok := s.Identity(rotated.AccessToken); !ok || got != id {
				t.Fatal("identity changed")
			}
			if _, err := s.Refresh(pair.RefreshToken); !errors.Is(err, ErrCredential) {
				t.Fatal("replay accepted")
			}
			if _, ok := s.Identity(rotated.AccessToken); ok {
				t.Fatal("replay did not revoke session")
			}
			if _, err := s.Refresh(rotated.RefreshToken); !errors.Is(err, ErrCredential) {
				t.Fatal("revoked refresh accepted")
			}
		})
	}
}

func TestConcurrentRefreshIsSingleUse(t *testing.T) {
	s, _ := testStore()
	pair, _ := s.Login("public", "public-secret")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Refresh(pair.RefreshToken); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 || len(s.sessions) != 0 {
		t.Fatalf("success=%d sessions=%d", success, len(s.sessions))
	}
}

func TestCapacityAndAbsoluteRefreshExpiry(t *testing.T) {
	s, now := testStore()
	pair, _ := s.Login("public", "public-secret")
	s.Login("analyst", "analyst-secret")
	if _, err := s.Login("public", "public-secret"); !errors.Is(err, ErrCapacity) {
		t.Fatal("capacity", err)
	}
	*now = now.Add(59*time.Minute + 30*time.Second)
	rotated, err := s.Refresh(pair.RefreshToken)
	if err != nil || rotated.ExpiresIn != 30 || rotated.RefreshExpiresIn != 30 {
		t.Fatal("absolute lifetime", rotated, err)
	}
	*now = now.Add(30 * time.Second)
	if _, err := s.Refresh(rotated.RefreshToken); !errors.Is(err, ErrCredential) {
		t.Fatal("expired refresh accepted")
	}
	if _, err := s.Login("public", "public-secret"); err != nil {
		t.Fatal("expired sessions not pruned", err)
	}
}
