package authinternal

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

type memoryStore struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
	sessions map[string]sessionState
}

func (s *memoryStore) SaveAttempt(_ context.Context, k string, a loginAttempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts[k] = a
	return nil
}
func (s *memoryStore) ConsumeAttempt(_ context.Context, k, p string) (loginAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.attempts[k]
	if !ok || a.Provider != p || !a.Expires.After(time.Now()) {
		return a, sql.ErrNoRows
	}
	delete(s.attempts, k)
	return a, nil
}
func (s *memoryStore) SaveSession(_ context.Context, k string, state sessionState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[k] = state
	return nil
}
func (s *memoryStore) UseSession(_ context.Context, k, replacement string, now time.Time, idleTimeout, renewalInterval time.Duration) (providerIdentity, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.sessions[k]
	if !ok || !state.Expires.After(now) || !state.IdleExpires.After(now) || (state.ReplacedAt != nil && !state.ReplacedAt.After(now.Add(-renewalGracePeriod))) {
		return providerIdentity{}, false, sql.ErrNoRows
	}
	if state.ReplacedAt != nil {
		return state.Identity, false, nil
	}
	state.LastSeen = now
	state.IdleExpires = now.Add(idleTimeout)
	if state.IdleExpires.After(state.Expires) {
		state.IdleExpires = state.Expires
	}
	if replacement != "" && !now.Before(state.RenewAfter) {
		next := state
		next.RenewAfter = now.Add(renewalInterval)
		next.ReplacedAt = nil
		replaced := now
		state.ReplacedAt = &replaced
		s.sessions[k] = state
		s.sessions[replacement] = next
		return state.Identity, true, nil
	}
	s.sessions[k] = state
	return state.Identity, false, nil
}
func (s *memoryStore) DeleteSession(_ context.Context, k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	family := s.sessions[k].FamilyHash
	for hash, state := range s.sessions {
		if state.FamilyHash == family {
			delete(s.sessions, hash)
		}
	}
	return nil
}

func testSession(identity providerIdentity, expires time.Time) sessionState {
	now := time.Now()
	return sessionState{Identity: identity, FamilyHash: "family", Expires: expires, LastSeen: now, IdleExpires: expires, RenewAfter: expires}
}

func testOAuth(t *testing.T, name string) (*OAuth, *memoryStore, *gin.Engine) {
	t.Helper()
	cfg := OAuthConfig{Origin: "https://telemetry.example.com", DatabaseURL: "postgres://test?sslmode=verify-full", GoogleClientID: "client", GoogleClientSecret: "secret", GitHubClientID: "client", GitHubClientSecret: "secret", Grants: []Grant{{Provider: name, Subject: "42", OrganizationID: "tenant-a", Permissions: []string{TracesRead}}}}
	store := &memoryStore{attempts: map[string]loginAttempt{}, sessions: map[string]sessionState{}}
	o := newOAuth(cfg, store)
	provider := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			r.ParseForm()
			if r.Form.Get("code_verifier") == "" || r.Form.Get("client_secret") != "secret" || r.Form.Get("redirect_uri") != cfg.Origin+"/api/v1/auth/"+name+"/callback" {
				t.Error("missing verifier, credentials, or redirect URI")
			}
			w.Write([]byte(`{"access_token":"provider-secret","token_type":"Bearer"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer provider-secret" {
			t.Error("profile was not authenticated")
		}
		if name == "google" {
			w.Write([]byte(`{"sub":"42","name":"Test user","hd":"example.com","email_verified":true}`))
		} else {
			w.Write([]byte(`{"id":42,"login":"test-user","name":"Test user"}`))
		}
	})
	o.client.Transport = handlerTransport{provider}
	p := o.providers[name]
	p.config.Endpoint.TokenURL = "https://provider.test/token"
	p.profileURL = "https://provider.test/profile"
	o.providers[name] = p
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/auth/:provider/login", o.Start)
	router.GET("/api/v1/auth/:provider/callback", o.Callback)
	router.GET("/api/v1/auth/session", o.Session)
	router.POST("/api/v1/auth/logout", o.Logout)
	return o, store, router
}
func startLogin(t *testing.T, r *gin.Engine, name string) (string, *http.Cookie) {
	t.Helper()
	out := httptest.NewRecorder()
	r.ServeHTTP(out, httptest.NewRequest("GET", "/api/v1/auth/"+name+"/login", nil))
	if out.Code != 302 {
		t.Fatalf("start: %d %s", out.Code, out.Body)
	}
	u, _ := url.Parse(out.Header().Get("Location"))
	if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("code_challenge") == "" {
		t.Fatal("missing PKCE")
	}
	cookies := out.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/" {
		t.Fatal("unsafe login cookie")
	}
	return u.Query().Get("state"), cookies[0]
}
func callback(r *gin.Engine, name, state string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/v1/auth/"+name+"/callback?code=valid&state="+url.QueryEscape(state), nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	return out
}
func TestOAuthLoginLogout(t *testing.T) {
	for _, name := range []string{"google", "github"} {
		t.Run(name, func(t *testing.T) {
			o, store, r := testOAuth(t, name)
			state, cookie := startLogin(t, r, name)
			if _, ok := store.attempts[state]; ok {
				t.Fatal("raw state stored")
			}
			attempt := store.attempts[tokenHash(state)]
			if attempt.Verifier == "" {
				t.Fatal("missing verifier")
			}
			if callback(r, name, state, nil).Header().Get("Location") != "/?auth_error=invalid_login" {
				t.Fatal("unbound callback accepted")
			}
			out := callback(r, name, state, cookie)
			if out.Header().Get("Location") != "/" {
				t.Fatalf("callback failed: %s", out.Header().Get("Location"))
			}
			var session *http.Cookie
			for _, c := range out.Result().Cookies() {
				if c.Name == o.cookieName("session") {
					session = c
				}
			}
			if session == nil || !session.Secure || !session.HttpOnly || session.MaxAge != 28800 {
				t.Fatal("missing secure session")
			}
			if len(store.sessions) != 1 {
				t.Fatal("session not persisted")
			}
			if _, ok := store.sessions[session.Value]; ok {
				t.Fatal("raw session token stored")
			}
			req := httptest.NewRequest("GET", "/api/v1/meta?tenant=attacker", nil)
			req.AddCookie(session)
			actor, err := o.Authenticate(req)
			if err != nil {
				t.Fatal(err)
			}
			if actor.Subject != name+":42" || actor.Tenant != "tenant-a" || !HasPermission(actor, TracesRead) || HasPermission(actor, LogsRead) {
				t.Fatalf("incorrect authorization: %+v", actor)
			}
			if callback(r, name, state, cookie).Header().Get("Location") != "/?auth_error=invalid_login" {
				t.Fatal("callback replay accepted")
			}
			logout := func(origin string) int {
				req := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
				req.AddCookie(session)
				req.Header.Set("Origin", origin)
				req.Header.Set("X-Telemetry-CSRF", "1")
				out := httptest.NewRecorder()
				r.ServeHTTP(out, req)
				return out.Code
			}
			if logout("https://attacker.example") != 403 || authenticatedSubject(t, o, req) == "" {
				t.Fatal("cross-origin logout accepted")
			}
			if logout(o.config.Origin) != 204 || authenticatedSubject(t, o, req) != "" {
				t.Fatal("session not revoked")
			}
		})
	}
}
func TestOAuthRejectsExpiredAttemptsAndDeniedAccounts(t *testing.T) {
	o, store, r := testOAuth(t, "google")
	state, cookie := startLogin(t, r, "google")
	a := store.attempts[tokenHash(state)]
	a.Expires = time.Now().Add(-time.Second)
	store.attempts[tokenHash(state)] = a
	if callback(r, "google", state, cookie).Header().Get("Location") != "/?auth_error=invalid_login" {
		t.Fatal("expired login accepted")
	}
	state, cookie = startLogin(t, r, "google")
	o.config.Grants = nil
	if callback(r, "google", state, cookie).Header().Get("Location") != "/?auth_error=access_denied" || len(store.sessions) != 0 {
		t.Fatal("unlisted account accepted")
	}
}
func TestSessionExpiryAndGrantRemoval(t *testing.T) {
	o, store, _ := testOAuth(t, "google")
	token, _ := randomToken()
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: o.cookieName("session"), Value: token})
	a := providerIdentity{Provider: "google", Subject: "42"}
	store.SaveSession(context.Background(), tokenHash(token), testSession(a, time.Now().Add(-time.Second)))
	if authenticatedSubject(t, o, req) != "" {
		t.Fatal("expired session accepted")
	}
	store.SaveSession(context.Background(), tokenHash(token), testSession(a, time.Now().Add(time.Hour)))
	o.config.Grants = nil
	if authenticatedSubject(t, o, req) != "" {
		t.Fatal("removed access grant still accepted")
	}
}

func TestSessionIdleTimeoutAndRenewal(t *testing.T) {
	o, store, router := testOAuth(t, "google")
	token, _ := randomToken()
	hash := tokenHash(token)
	a := providerIdentity{Provider: "google", Subject: "42"}
	now := time.Now()
	state := sessionState{
		Identity: a, FamilyHash: hash, Expires: now.Add(time.Hour), LastSeen: now.Add(-time.Hour),
		IdleExpires: now.Add(-time.Second), RenewAfter: now.Add(-time.Minute),
	}
	store.SaveSession(context.Background(), hash, state)
	request := httptest.NewRequest("GET", "/api/v1/auth/session", nil)
	request.AddCookie(&http.Cookie{Name: o.cookieName("session"), Value: token})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("idle session remained authenticated", response.Code)
	}

	state.IdleExpires = now.Add(time.Hour)
	store.SaveSession(context.Background(), hash, state)
	request = httptest.NewRequest("GET", "/api/v1/auth/session", nil)
	request.AddCookie(&http.Cookie{Name: o.cookieName("session"), Value: token})
	if authenticatedSubject(t, o, request) != "google:42" || len(store.sessions) != 1 {
		t.Fatal("read-only authentication unexpectedly rotated the session")
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal("renewal failed", response.Code)
	}
	var renewed *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == o.cookieName("session") {
			renewed = cookie
		}
	}
	if renewed == nil || renewed.Value == token || len(store.sessions) != 2 {
		t.Fatal("session identifier was not rotated")
	}
	old := store.sessions[hash]
	replaced := time.Now().Add(-renewalGracePeriod - time.Second)
	old.ReplacedAt = &replaced
	store.sessions[hash] = old
	if authenticatedSubject(t, o, request) != "" {
		t.Fatal("replaced session survived the grace period")
	}
	logout := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	logout.AddCookie(renewed)
	logout.Header.Set("Origin", o.config.Origin)
	logout.Header.Set("X-Telemetry-CSRF", "1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, logout)
	if response.Code != http.StatusNoContent || len(store.sessions) != 0 {
		t.Fatal("logout did not revoke the renewed session family")
	}
}
func TestAccessGrants(t *testing.T) {
	cfg := OAuthConfig{Grants: []Grant{
		{Provider: "google", GoogleDomain: "example.com", OrganizationID: "domain", Permissions: []string{LogsRead}},
		{Provider: "google", Subject: "42", OrganizationID: "specific", Permissions: []string{TracesRead}},
	}}
	if got := cfg.principal(providerIdentity{Provider: "google", Subject: "42", GoogleDomain: "example.com"}); got.Tenant != "specific" || HasPermission(got, LogsRead) {
		t.Fatal("subject override merged domain access")
	}
	if cfg.principal(providerIdentity{Provider: "google", Subject: "43", GoogleDomain: "example.com"}).Tenant != "domain" {
		t.Fatal("domain grant missing")
	}
	if cfg.principal(providerIdentity{Provider: "github", Subject: "43", GoogleDomain: "example.com"}).Subject != "" {
		t.Fatal("domain crossed providers")
	}
	if cfg.principal(providerIdentity{Provider: "google", Subject: "43"}).Subject != "" {
		t.Fatal("missing hosted domain accepted")
	}
}

func TestOrganizationIdentityMappings(t *testing.T) {
	organizations := []OrganizationConfig{{
		ID: "platform", Name: "Platform Team", TelemetryScope: "platform-prod",
		IdentityMappings: []IdentityMapping{{
			Provider: "google", Match: IdentityMatch{Domain: "example.com"}, Permissions: []string{TracesRead},
		}},
	}}
	cfg := OAuthConfig{
		Origin: "https://example.com", DatabaseURL: "postgres://test?sslmode=verify-full",
		GoogleClientID: "id", GoogleClientSecret: "secret", Organizations: organizations,
		Grants: grantsFromOrganizations(organizations),
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	principal := cfg.principal(providerIdentity{Provider: "google", Subject: "42", GoogleDomain: "example.com"})
	if principal.OrganizationID != "platform" || principal.OrganizationName != "Platform Team" || principal.OrganizationScope != "platform-prod" {
		t.Fatalf("organization metadata was not applied: %#v", principal)
	}
	organizations[0].IdentityMappings[0].Match.Subject = "42"
	cfg.Organizations = organizations
	cfg.Grants = grantsFromOrganizations(organizations)
	if cfg.Validate() == nil {
		t.Fatal("ambiguous identity mapping accepted")
	}
}

func TestLoadOrganizationConfigFile(t *testing.T) {
	path := t.TempDir() + "/organizations.json"
	raw := `[{"id":"platform","name":"Platform Team","telemetryScope":"platform-prod","identityMappings":[{"provider":"google","match":{"email":"member@example.com"},"permissions":["observability:logs:read"]}],"dataSources":{"unified":["primary"],"traces":[],"logs":[]}}]`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AUTH_ORGANIZATIONS_FILE", path)
	t.Setenv("AUTH_GRANTS_FILE", "")
	t.Setenv("AUTH_ORIGIN", "https://telemetry.example.com")
	t.Setenv("DATABASE_URL", "postgres://test?sslmode=verify-full")
	t.Setenv("GOOGLE_CLIENT_ID", "id")
	t.Setenv("GOOGLE_CLIENT_SECRET", "secret")
	cfg, err := LoadOAuthConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Organizations) != 1 || len(cfg.Grants) != 1 || cfg.Grants[0].GoogleEmail != "member@example.com" || cfg.Grants[0].OrganizationID != "platform" {
		t.Fatalf("organization config was not flattened: %#v", cfg)
	}
}

func TestOAuthConfigValidation(t *testing.T) {
	valid := OAuthConfig{Origin: "https://example.com", DatabaseURL: "postgres://test?sslmode=verify-full", GoogleClientID: "id", GoogleClientSecret: "secret"}
	for _, origin := range []string{"http://example.com", "https://example.com/path", "https://user@example.com", "https://example.com?x=y"} {
		cfg := valid
		cfg.Origin = origin
		if cfg.Validate() == nil {
			t.Fatalf("accepted origin %s", origin)
		}
	}
	for _, origin := range []string{"http://localhost:5173", "http://127.0.0.1:8080", "https://example.com"} {
		cfg := valid
		cfg.Origin = origin
		if err := cfg.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	cfg := valid
	cfg.GoogleClientSecret = ""
	if cfg.Validate() == nil {
		t.Fatal("partial credentials accepted")
	}
	cfg = valid
	cfg.Grants = []Grant{{Provider: "google", Subject: "42", GoogleDomain: "example.com", OrganizationID: "x"}}
	if cfg.Validate() == nil {
		t.Fatal("ambiguous grant accepted")
	}
	for _, lifetime := range []time.Duration{time.Minute, 13 * time.Hour} {
		cfg = valid
		cfg.SessionLifetime = lifetime
		if cfg.Validate() == nil {
			t.Fatalf("accepted unsafe session lifetime %s", lifetime)
		}
	}
	for _, tc := range []struct {
		idle, renewal time.Duration
	}{
		{time.Minute, 30 * time.Second},
		{13 * time.Hour, 15 * time.Minute},
		{30 * time.Minute, time.Minute},
		{30 * time.Minute, 30 * time.Minute},
	} {
		cfg = valid
		cfg.IdleTimeout = tc.idle
		cfg.RenewalInterval = tc.renewal
		if cfg.Validate() == nil {
			t.Fatalf("accepted unsafe idle/renewal settings: %s/%s", tc.idle, tc.renewal)
		}
	}
	cfg = valid
	cfg.DatabaseURL = "postgres://test?sslmode=disable"
	if cfg.Validate() == nil {
		t.Fatal("HTTPS origin accepted insecure session database")
	}
}
func TestPKCEVerifierNotInRedirect(t *testing.T) {
	o, store, r := testOAuth(t, "google")
	state, _ := startLogin(t, r, "google")
	a := store.attempts[tokenHash(state)]
	provider := o.providers["google"]
	raw := provider.config.AuthCodeURL(state, oauth2.S256ChallengeOption(a.Verifier))
	if strings.Contains(raw, a.Verifier) {
		t.Fatal("verifier leaked")
	}
	b, _ := json.Marshal(store.sessions)
	if strings.Contains(string(b), "provider-secret") {
		t.Fatal("provider token persisted")
	}
}

type handlerTransport struct{ handler http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	out := httptest.NewRecorder()
	t.handler.ServeHTTP(out, r)
	return out.Result(), nil
}
func (s *memoryStore) Cleanup(context.Context) error { return nil }

func authenticatedSubject(t *testing.T, o *OAuth, r *http.Request) string {
	t.Helper()
	actor, err := o.Authenticate(r)
	if err != nil {
		t.Fatal(err)
	}
	return actor.Subject
}

func (s *memoryStore) Ping(context.Context) error { return nil }

func TestOAuthProviderBindingAndFixation(t *testing.T) {
	o, store, r := testOAuth(t, "google")
	state, cookie := startLogin(t, r, "google")
	if callback(r, "github", state, cookie).Header().Get("Location") != "/?auth_error=invalid_login" {
		t.Fatal("provider mixup accepted")
	}
	old, _ := randomToken()
	store.SaveSession(context.Background(), tokenHash(old), testSession(providerIdentity{Provider: "google", Subject: "42"}, time.Now().Add(time.Hour)))
	req := httptest.NewRequest("GET", "/api/v1/auth/google/callback?code=valid&state="+state, nil)
	req.AddCookie(cookie)
	req.AddCookie(&http.Cookie{Name: o.cookieName("session"), Value: old})
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	if out.Header().Get("Location") != "/" {
		t.Fatal("reauthentication failed")
	}
	if _, ok := store.sessions[tokenHash(old)]; ok || len(store.sessions) != 1 {
		t.Fatal("old session survived reauthentication")
	}
	for _, c := range out.Result().Cookies() {
		if c.Name == o.cookieName("session") && c.Value == old {
			t.Fatal("session identifier reused")
		}
	}
}

func TestUnverifiedGoogleDomainDenied(t *testing.T) {
	o, _, _ := testOAuth(t, "google")
	o.client.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"sub":"43","hd":"example.com","email_verified":false}`))
	})}
	o.config.Grants = []Grant{{Provider: "google", GoogleDomain: "example.com", OrganizationID: "team"}}
	a, err := o.fetchIdentity(context.Background(), "google", "https://provider.test/profile", "token")
	if err != nil {
		t.Fatal(err)
	}
	if a.GoogleDomain != "" || o.config.principal(a).Subject != "" {
		t.Fatal("unverified domain accepted")
	}
}

func TestLogoutRequiresCSRFHeaderAndOrigin(t *testing.T) {
	o, _, r := testOAuth(t, "google")
	for _, headers := range []map[string]string{{}, {"Origin": o.config.Origin}, {"X-Telemetry-CSRF": "1"}} {
		req := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if out.Code != 403 {
			t.Fatal("incomplete CSRF checks accepted")
		}
	}
}

func TestGoogleEmailGrantRequiresVerifiedProfile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile string
		allowed bool
	}{
		{"verified", `{"sub":"43","email":"Member@gmail.com","email_verified":true}`, true},
		{"unverified", `{"sub":"43","email":"member@gmail.com","email_verified":false}`, false},
		{"missing_verification", `{"sub":"43","email":"member@gmail.com"}`, false},
		{"different_email", `{"sub":"43","email":"other@gmail.com","email_verified":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, store, _ := testOAuth(t, "google")
			o.config.Grants = []Grant{{Provider: "google", GoogleEmail: "member@gmail.com", OrganizationID: "local", Permissions: []string{TracesRead}}}
			if err := o.config.Validate(); err != nil {
				t.Fatal(err)
			}
			o.client.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.profile))
			})}
			a, err := o.fetchIdentity(context.Background(), "google", "https://provider.test/profile", "token")
			if err != nil {
				t.Fatal(err)
			}
			if got := o.config.principal(a); (got.Subject != "") != tc.allowed {
				t.Fatal("unexpected access", got)
			}
			token, _ := randomToken()
			store.SaveSession(context.Background(), tokenHash(token), testSession(a, time.Now().Add(time.Hour)))
			request := httptest.NewRequest("GET", "/", nil)
			request.AddCookie(&http.Cookie{Name: o.cookieName("session"), Value: token})
			actor, err := o.Authenticate(request)
			if err != nil || (actor.Subject != "") != tc.allowed {
				t.Fatal("session lost email grant", actor, err)
			}
		})
	}
}
