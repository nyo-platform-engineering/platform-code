package auth

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

type OAuth struct {
	config    OAuthConfig
	store     sessionStore
	providers map[string]oauthProvider
	client    *http.Client
	// Bound unauthenticated login starts per process without retaining IP addresses.
	mu     sync.Mutex
	window time.Time
	starts int
}

func newOAuth(config OAuthConfig, store sessionStore) *OAuth {
	return &OAuth{config: config, store: store, providers: configuredProviders(config), client: &http.Client{Timeout: 10 * time.Second}}
}

// Expiry is checked on every read; cleanup only reclaims old rows.
func (o *OAuth) RunCleanup(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := o.store.Cleanup(cleanup)
			cancel()
			if err != nil && ctx.Err() == nil {
				logger.Error("expired auth rows could not be removed")
			}
		}
	}
}

func (o *OAuth) Ready(ctx context.Context) error { return o.store.Ping(ctx) }
