package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"
)

// startAuth owns the PostgreSQL pool and background cleanup until shutdown.
func startAuth(ctx context.Context, cfg auth.OAuthConfig, logger *slog.Logger) (*auth.OAuth, func(), error) {
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	db, err := database.Open(startup, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	if err := database.Migrate(startup, db); err != nil {
		database.Close(db)
		return nil, nil, err
	}
	oauth, err := auth.NewOAuth(cfg, db)
	if err != nil {
		database.Close(db)
		return nil, nil, err
	}

	cleanup, stopCleanup := context.WithCancel(ctx)
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		oauth.RunCleanup(cleanup, logger)
	}()
	closeAuth := func() {
		stopCleanup()
		<-cleanupDone
		database.Close(db)
	}
	return oauth, closeAuth, nil
}
