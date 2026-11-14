package authinternal

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const renewalGracePeriod = 30 * time.Second

func (o *OAuth) Authenticate(r *http.Request) (Principal, error) {
	return o.authenticate(nil, r, false)
}

// AuthenticateAndRenew rotates a due session identifier on authenticated API
// responses. The absolute expiry is retained; renewal never extends the login.
func (o *OAuth) AuthenticateAndRenew(w http.ResponseWriter, r *http.Request) (Principal, error) {
	return o.authenticate(w, r, true)
}

func (o *OAuth) authenticate(w http.ResponseWriter, r *http.Request, renew bool) (Principal, error) {
	cookie, err := r.Cookie(o.cookieName("session"))
	if err != nil || len(cookie.Value) != 43 {
		return Principal{}, nil
	}
	replacementToken := ""
	if renew {
		replacementToken, err = randomToken()
		if err != nil {
			return Principal{}, err
		}
	}
	replacementHash := ""
	if replacementToken != "" {
		replacementHash = tokenHash(replacementToken)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	identity, rotated, err := o.store.UseSession(ctx, tokenHash(cookie.Value), replacementHash, time.Now(), o.config.idleTimeout(), o.config.renewalInterval())
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, nil
	}
	if err != nil {
		return Principal{}, err
	}
	if rotated && w != nil {
		o.setCookie(w, "session", replacementToken, int(o.config.sessionLifetime().Seconds()))
	}
	return o.principal(ctx, identity)
}

func (o *OAuth) Session(c *gin.Context) {
	actor, err := o.AuthenticateAndRenew(c.Writer, c.Request)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth_unavailable"})
		return
	}
	if actor.Subject == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"actor": actor})
}

func (o *OAuth) Logout(c *gin.Context) {
	// Exact configured origin avoids trusting Host or proxy headers for CSRF checks.
	if c.GetHeader("Origin") != o.config.Origin || c.GetHeader("X-Telemetry-CSRF") != "1" {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	if cookie, err := c.Request.Cookie(o.cookieName("session")); err == nil {
		if err := o.store.DeleteSession(ctx, tokenHash(cookie.Value)); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth_unavailable"})
			return
		}
	}
	o.cookie(c, "session", "", -1)
	o.cookie(c, "state", "", -1)
	c.Status(http.StatusNoContent)
}
