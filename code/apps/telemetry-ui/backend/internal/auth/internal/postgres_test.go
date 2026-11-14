package authinternal

import (
	"context"
	"database/sql"
	"errors"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"
	"os"
	"testing"
	"time"
)

func TestPostgresSessions(t *testing.T) {
	dsn := os.Getenv("AUTH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set AUTH_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := OAuthConfig{
		Origin: "http://127.0.0.1:5173", DatabaseURL: dsn, GoogleClientID: "test", GoogleClientSecret: "test",
		Grants: []Grant{{Provider: "google", Subject: "42", OrganizationID: "test-org", Permissions: []string{TracesRead}}},
	}
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(db)
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	if err := BootstrapGrants(ctx, db, cfg.Grants); err != nil {
		t.Fatal(err)
	}
	if err := BootstrapGrants(ctx, db, cfg.Grants); err != nil {
		t.Fatal("grant bootstrap not idempotent", err)
	}
	for _, item := range []struct {
		model any
		index string
	}{
		{&database.LoginAttempt{}, "telemetry_login_attempts_expiry"},
		{&database.Session{}, "telemetry_sessions_expiry"},
	} {
		if !db.Migrator().HasIndex(item.model, item.index) {
			t.Fatalf("missing expiry index %s", item.index)
		}
	}
	first, err := NewOAuth(cfg, db)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := first.principal(ctx, providerIdentity{Provider: "google", Subject: "42", Name: "Test"})
	if err != nil || principal.OrganizationID != "test-org" || principal.OrganizationScope != "test-org" || !HasPermission(principal, MetadataRead) || !HasPermission(principal, TracesRead) {
		t.Fatal("control-plane grant was not resolved", principal, err)
	}
	secondDB, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close(secondDB)
	second, err := NewOAuth(cfg, secondDB)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := randomToken()
	hash := tokenHash(token)
	defer first.store.DeleteSession(ctx, hash)
	if err := first.store.SaveAttempt(ctx, hash, loginAttempt{Provider: "google", Verifier: "test", Expires: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := second.store.ConsumeAttempt(ctx, hash, "github"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("provider mixup accepted", err)
	}
	if _, err := second.store.ConsumeAttempt(ctx, hash, "google"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.store.ConsumeAttempt(ctx, hash, "google"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("state replay accepted", err)
	}
	a := providerIdentity{Provider: "google", Subject: "42", Name: "Test", GoogleEmail: "member@gmail.com"}
	now := time.Now()
	state := sessionState{Identity: a, FamilyHash: hash, Expires: now.Add(time.Hour), LastSeen: now, IdleExpires: now.Add(time.Minute), RenewAfter: now.Add(time.Minute)}
	if err := first.store.SaveSession(ctx, hash, state); err != nil {
		t.Fatal(err)
	}
	if got, _, err := second.store.UseSession(ctx, hash, "", time.Now(), time.Minute, time.Minute); err != nil || got != a {
		t.Fatal("session not shared", got, err)
	}
	if err := second.store.DeleteSession(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.store.UseSession(ctx, hash, "", time.Now(), time.Minute, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("logout not shared", err)
	}
	state.Expires = time.Now().Add(-time.Second)
	state.IdleExpires = state.Expires
	if err := first.store.SaveSession(ctx, hash, state); err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.store.UseSession(ctx, hash, "", time.Now(), time.Minute, time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("expired session accepted", err)
	}
}
