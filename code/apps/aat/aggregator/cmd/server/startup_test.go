package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDatabaseStartupRetriesTransientFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	attempts := 0
	err := waitForDatabase(ctx, func(attempt context.Context) error {
		attempts++
		if deadline, ok := attempt.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
			t.Fatal("missing bounded connection attempt")
		}
		if attempts == 1 {
			return errors.New("connection refused")
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
}

func TestDatabaseStartupStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := waitForDatabase(ctx, func(context.Context) error {
		cancel()
		return errors.New("connection refused")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
