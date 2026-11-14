package authinternal

import (
	"context"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

const attemptLifetime = 10 * time.Minute

func (o *OAuth) Start(c *gin.Context) {
	providerName := c.Param("provider")
	configuredProvider, ok := o.providers[providerName]
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider_unavailable"})
		return
	}

	if !o.allowLogin() {
		c.Header("Retry-After", "60")
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too_many_logins"})
		return
	}
	state, err := randomToken()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth_unavailable"})
		return
	}
	attempt := loginAttempt{Provider: providerName, Verifier: oauth2.GenerateVerifier(), Expires: time.Now().Add(attemptLifetime)}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	if err := o.store.SaveAttempt(ctx, tokenHash(state), attempt); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth_unavailable"})
		return
	}
	o.cookie(c, "state", state, int(attemptLifetime.Seconds()))
	c.Redirect(http.StatusFound, configuredProvider.config.AuthCodeURL(state, oauth2.S256ChallengeOption(attempt.Verifier)))
}

func (o *OAuth) Callback(c *gin.Context) {
	providerName := c.Param("provider")
	configuredProvider, ok := o.providers[providerName]
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider_unavailable"})
		return
	}
	state := c.Query("state")
	stateCookie, err := c.Request.Cookie(o.cookieName("state"))
	if err != nil || !validOAuthState(state, stateCookie.Value) {
		redirectAuthError(c, "invalid_login")
		return
	}
	o.cookie(c, "state", "", -1)
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	attempt, err := o.store.ConsumeAttempt(ctx, tokenHash(state), providerName)
	if err != nil {
		redirectAuthError(c, "invalid_login")
		return
	}
	if c.Query("error") != "" || c.Query("code") == "" {
		redirectAuthError(c, "cancelled")
		return
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, o.client)
	token, err := configuredProvider.config.Exchange(ctx, c.Query("code"), oauth2.VerifierOption(attempt.Verifier))
	if err != nil {
		redirectAuthError(c, "login_failed")
		return
	}
	identity, err := o.fetchIdentity(ctx, providerName, configuredProvider.profileURL, token.AccessToken)
	if err != nil {
		redirectAuthError(c, "login_failed")
		return
	}
	principal, err := o.principal(ctx, identity)
	if err != nil {
		redirectAuthError(c, "login_failed")
		return
	}
	if principal.Subject == "" {
		redirectAuthError(c, "access_denied")
		return
	}
	sessionToken, err := randomToken()
	if err != nil {
		redirectAuthError(c, "login_failed")
		return
	}
	// Reauthentication always replaces the browser's previous session.
	if previousSession, err := c.Request.Cookie(o.cookieName("session")); err == nil {
		if err := o.store.DeleteSession(ctx, tokenHash(previousSession.Value)); err != nil {
			redirectAuthError(c, "login_failed")
			return
		}
	}
	lifetime := o.config.sessionLifetime()
	now := time.Now()
	sessionRecord := sessionState{
		Identity: identity, FamilyHash: tokenHash(sessionToken), Expires: now.Add(lifetime), LastSeen: now,
		IdleExpires: now.Add(o.config.idleTimeout()), RenewAfter: now.Add(o.config.renewalInterval()),
	}
	if err := o.store.SaveSession(ctx, tokenHash(sessionToken), sessionRecord); err != nil {
		redirectAuthError(c, "login_failed")
		return
	}
	o.cookie(c, "session", sessionToken, int(lifetime.Seconds()))
	c.Redirect(http.StatusSeeOther, "/")
}

func validOAuthState(queryState, cookieState string) bool {
	return len(queryState) == 43 && subtle.ConstantTimeCompare([]byte(queryState), []byte(cookieState)) == 1
}

func redirectAuthError(c *gin.Context, code string) {
	c.Redirect(http.StatusSeeOther, "/?auth_error="+code)
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
