package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"
	"gorm.io/gorm"
)

// startAuth owns the PostgreSQL pool and background cleanup until shutdown.
func startAuth(ctx context.Context, cfg auth.OAuthConfig, logger *slog.Logger) (*auth.OAuth, *gorm.DB, func(), error) {
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	db, err := database.Open(startup, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := database.Migrate(startup, db); err != nil {
		database.Close(db)
		return nil, nil, nil, err
	}
	var bootstrapErr error
	if len(cfg.Organizations) > 0 {
		bootstrapErr = auth.BootstrapControlPlane(startup, db, cfg.Organizations, cfg.Grants)
	} else {
		bootstrapErr = auth.BootstrapGrants(startup, db, cfg.Grants)
	}
	if bootstrapErr != nil {
		database.Close(db)
		return nil, nil, nil, bootstrapErr
	}
	oauth, err := auth.NewOAuth(cfg, db)
	if err != nil {
		database.Close(db)
		return nil, nil, nil, err
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
	return oauth, db, closeAuth, nil
}
