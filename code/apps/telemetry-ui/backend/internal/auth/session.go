package auth

import (
	"context"
	"database/sql"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"time"
)

const sessionLifetime = 12 * time.Hour

func (o *OAuth) Authenticate(r *http.Request) (Principal, error) {
	cookie, err := r.Cookie(o.cookieName("session"))
	if err != nil || len(cookie.Value) != 43 {
		return Principal{}, nil
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	a, err := o.store.Session(ctx, tokenHash(cookie.Value))
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, nil
	}
	if err != nil {
		return Principal{}, err
	}
	return o.cfg.principal(a), nil
}

func (o *OAuth) Session(c *gin.Context) {
	actor, err := o.Authenticate(c.Request)
	if err != nil {
		c.JSON(503, gin.H{"error": "auth_unavailable"})
		return
	}
	if actor.Subject == "" {
		c.JSON(401, gin.H{"error": "unauthenticated"})
		return
	}
	c.JSON(200, gin.H{"actor": actor})
}

func (o *OAuth) Logout(c *gin.Context) {
	// Exact configured origin avoids trusting Host or proxy headers for CSRF checks.
	if c.GetHeader("Origin") != o.cfg.Origin || c.GetHeader("X-Telemetry-CSRF") != "1" {
		c.JSON(403, gin.H{"error": "forbidden"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	if cookie, err := c.Request.Cookie(o.cookieName("session")); err == nil {
		if err := o.store.DeleteSession(ctx, tokenHash(cookie.Value)); err != nil {
			c.JSON(503, gin.H{"error": "auth_unavailable"})
			return
		}
	}
	o.cookie(c, "session", "", -1)
	o.cookie(c, "state", "", -1)
	c.Status(204)
}
