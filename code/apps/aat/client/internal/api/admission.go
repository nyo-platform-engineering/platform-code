package api

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"aat/internal/httpkit"
)

// clientIdentity is the Auth Service's introspection answer.
type clientIdentity = struct {
	Active bool   `json:"active"`
	ID     string `json:"client_id"`
}

// identityCache keeps recent positive introspections, keyed by a hash of the token,
// so a request can be classified before admission without a call to Auth, and a
// flood of Media requests does not turn into a flood of introspections. A revoked
// token stays usable for at most the TTL; access tokens live 60 s anyway.
type identityCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[[32]byte]cachedIdentity
}

type cachedIdentity struct {
	identity clientIdentity
	until    time.Time
}

const identityCacheLimit = 10000

func (c *identityCache) get(token string) (clientIdentity, bool) {
	if c == nil || c.ttl <= 0 {
		return clientIdentity{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[sha256.Sum256([]byte(token))]
	if !ok || time.Now().After(entry.until) {
		return clientIdentity{}, false
	}
	return entry.identity, true
}

func (c *identityCache) put(token string, identity clientIdentity) {
	if c == nil || c.ttl <= 0 || !identity.Active {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= identityCacheLimit {
		c.entries = map[[32]byte]cachedIdentity{}
	}
	c.entries[sha256.Sum256([]byte(token))] = cachedIdentity{identity, time.Now().Add(c.ttl)}
}

// introspectInto resolves a bearer token through the cache, then the Auth Service.
func (u upstream) introspectInto(ctx context.Context, token, correlation string, out *clientIdentity) error {
	if identity, ok := u.ids.get(token); ok {
		*out = identity
		return nil
	}
	if err := u.get(ctx, "POST", u.endpoint(u.config.AuthURL, "/internal/introspect"), u.config.AuthToken, correlation, map[string]string{"token": token}, out); err != nil {
		return err
	}
	u.ids.put(token, *out)
	return nil
}

// admission sheds load by priority. Every request needs a slot of the shared pool
// (size MaxConcurrent). Media and unidentified requests must also hold a slot of the
// smaller media pool, so they can never take the share reserved for the Field Team
// (and BNPB Internal). Nothing queues: a request that cannot get its slots at once is
// rejected with 429 and Retry-After, which keeps latency bounded under overload.
type admission struct {
	all   chan struct{}
	media chan struct{}
}

func newAdmission(maxConcurrent, mediaPercent int) *admission {
	media := max(1, maxConcurrent*mediaPercent/100)
	return &admission{all: make(chan struct{}, maxConcurrent), media: make(chan struct{}, min(media, maxConcurrent))}
}

func priorityClient(id string) bool { return id == "responder" || id == "analyst" }

func (a *admission) wrap(u upstream, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		class := "media"
		if parts := strings.Fields(r.Header.Get("Authorization")); len(parts) == 2 && parts[0] == "Bearer" && len(parts[1]) <= 256 {
			var identity clientIdentity
			ctx, cancel := context.WithTimeout(r.Context(), u.config.Timeout)
			err := u.introspectInto(ctx, parts[1], r.Header.Get("X-Correlation-ID"), &identity)
			cancel()
			if err == nil && identity.Active && priorityClient(identity.ID) {
				class = "priority"
			}
		}
		if class == "media" {
			select {
			case a.media <- struct{}{}:
				defer func() { <-a.media }()
			default:
				shed(w, r, class, "media share full")
				return
			}
		}
		select {
		case a.all <- struct{}{}:
			defer func() { <-a.all }()
		default:
			shed(w, r, class, "capacity full")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func shed(w http.ResponseWriter, r *http.Request, class, reason string) {
	slog.Info("request shed", "service", "Client API", "class", class, "reason", reason, "path", r.URL.Path,
		"correlation_id", r.Header.Get("X-Correlation-ID"))
	w.Header().Set("Retry-After", "1")
	httpkit.JSON(w, http.StatusTooManyRequests, map[string]string{"error": "overloaded; request shed, retry later"})
}
