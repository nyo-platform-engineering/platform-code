package auth

import (
	"context"
	"crypto/subtle"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
	"time"
)

const attemptLifetime = 10 * time.Minute

func (o *OAuth) Start(c *gin.Context) {
	name := c.Param("provider")
	p, ok := o.providers[name]
	if !ok {
		c.JSON(404, gin.H{"error": "provider_unavailable"})
		return
	}

	if !o.allowLogin() {
		c.Header("Retry-After", "60")
		c.JSON(429, gin.H{"error": "too_many_logins"})
		return
	}
	state, err := randomToken()
	if err != nil {
		c.JSON(503, gin.H{"error": "auth_unavailable"})
		return
	}
	attempt := loginAttempt{Provider: name, Verifier: oauth2.GenerateVerifier(), Expires: time.Now().Add(attemptLifetime)}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	if err := o.store.SaveAttempt(ctx, tokenHash(state), attempt); err != nil {
		c.JSON(503, gin.H{"error": "auth_unavailable"})
		return
	}
	o.cookie(c, "state", state, int(attemptLifetime.Seconds()))
	c.Redirect(302, p.config.AuthCodeURL(state, oauth2.S256ChallengeOption(attempt.Verifier)))
}

func (o *OAuth) Callback(c *gin.Context) {
	name := c.Param("provider")
	p, ok := o.providers[name]
	if !ok {
		c.JSON(404, gin.H{"error": "provider_unavailable"})
		return
	}
	state := c.Query("state")
	cookie, err := c.Request.Cookie(o.cookieName("state"))
	if err != nil || len(state) != 43 || subtle.ConstantTimeCompare([]byte(state), []byte(cookie.Value)) != 1 {
		c.Redirect(303, "/?auth_error=invalid_login")
		return
	}
	o.cookie(c, "state", "", -1)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	attempt, err := o.store.ConsumeAttempt(ctx, tokenHash(state), name)
	if err != nil {
		c.Redirect(303, "/?auth_error=invalid_login")
		return
	}
	if c.Query("error") != "" || c.Query("code") == "" {
		c.Redirect(303, "/?auth_error=cancelled")
		return
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, o.client)
	token, err := p.config.Exchange(ctx, c.Query("code"), oauth2.VerifierOption(attempt.Verifier))
	if err != nil {
		c.Redirect(303, "/?auth_error=login_failed")
		return
	}
	a, err := o.profile(ctx, name, p.profileURL, token.AccessToken)
	if err != nil {
		c.Redirect(303, "/?auth_error=login_failed")
		return
	}
	if o.cfg.principal(a).Subject == "" {
		c.Redirect(303, "/?auth_error=access_denied")
		return
	}
	session, err := randomToken()
	if err != nil {
		c.Redirect(303, "/?auth_error=login_failed")
		return
	}
	// Reauthentication always replaces the browser's previous session.
	if old, err := c.Request.Cookie(o.cookieName("session")); err == nil {
		if err := o.store.DeleteSession(ctx, tokenHash(old.Value)); err != nil {
			c.Redirect(303, "/?auth_error=login_failed")
			return
		}
	}
	if err := o.store.SaveSession(ctx, tokenHash(session), a, time.Now().Add(sessionLifetime)); err != nil {
		c.Redirect(303, "/?auth_error=login_failed")
		return
	}
	o.cookie(c, "session", session, int(sessionLifetime.Seconds()))
	c.Redirect(303, "/")
}

func (o *OAuth) allowLogin() bool {
	o.mu.Lock()
	if time.Since(o.window) >= time.Minute {
		o.window = time.Now()
		o.starts = 0
	}
	allowed := o.starts < 60
	if allowed {
		o.starts++
	}
	o.mu.Unlock()
	return allowed
}
