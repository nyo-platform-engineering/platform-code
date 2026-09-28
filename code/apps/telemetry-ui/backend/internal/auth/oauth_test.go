package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

type storedSession struct {
	account account
	expires time.Time
}
type memoryStore struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
	sessions map[string]storedSession
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
func (s *memoryStore) SaveSession(_ context.Context, k string, a account, e time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[k] = storedSession{a, e}
	return nil
}
func (s *memoryStore) Session(_ context.Context, k string) (account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.sessions[k]
	if !ok || !a.expires.After(time.Now()) {
		return account{}, sql.ErrNoRows
	}
	return a.account, nil
}
func (s *memoryStore) DeleteSession(_ context.Context, k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, k)
	return nil
}

func testOAuth(t *testing.T, name string) (*OAuth, *memoryStore, *gin.Engine) {
	t.Helper()
	cfg := OAuthConfig{Origin: "https://telemetry.example.com", DatabaseURL: "postgres://test", GoogleClientID: "client", GoogleClientSecret: "secret", GitHubClientID: "client", GitHubClientSecret: "secret", Grants: []Grant{{Provider: name, Subject: "42", Tenant: "tenant-a", Permissions: []string{TracesRead}}}}
	store := &memoryStore{attempts: map[string]loginAttempt{}, sessions: map[string]storedSession{}}
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
			if session == nil || !session.Secure || !session.HttpOnly || session.MaxAge != 43200 {
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
			if logout(o.cfg.Origin) != 204 || authenticatedSubject(t, o, req) != "" {
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
	o.cfg.Grants = nil
	if callback(r, "google", state, cookie).Header().Get("Location") != "/?auth_error=access_denied" || len(store.sessions) != 0 {
		t.Fatal("unlisted account accepted")
	}
}
func TestSessionExpiryAndGrantRemoval(t *testing.T) {
	o, store, _ := testOAuth(t, "google")
	token, _ := randomToken()
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: o.cookieName("session"), Value: token})
	a := account{Provider: "google", Subject: "42"}
	store.SaveSession(context.Background(), tokenHash(token), a, time.Now().Add(-time.Second))
	if authenticatedSubject(t, o, req) != "" {
		t.Fatal("expired session accepted")
	}
	store.SaveSession(context.Background(), tokenHash(token), a, time.Now().Add(time.Hour))
	o.cfg.Grants = nil
	if authenticatedSubject(t, o, req) != "" {
		t.Fatal("removed access grant still accepted")
	}
}
func TestAccessGrants(t *testing.T) {
	cfg := OAuthConfig{Grants: []Grant{
		{Provider: "google", GoogleDomain: "example.com", Tenant: "domain", Permissions: []string{LogsRead}},
		{Provider: "google", Subject: "42", Tenant: "specific", Permissions: []string{TracesRead}},
	}}
	if got := cfg.principal(account{Provider: "google", Subject: "42", GoogleDomain: "example.com"}); got.Tenant != "specific" || HasPermission(got, LogsRead) {
		t.Fatal("subject override merged domain access")
	}
	if cfg.principal(account{Provider: "google", Subject: "43", GoogleDomain: "example.com"}).Tenant != "domain" {
		t.Fatal("domain grant missing")
	}
	if cfg.principal(account{Provider: "github", Subject: "43", GoogleDomain: "example.com"}).Subject != "" {
		t.Fatal("domain crossed providers")
	}
	if cfg.principal(account{Provider: "google", Subject: "43"}).Subject != "" {
		t.Fatal("missing hosted domain accepted")
	}
}
func TestOAuthConfigValidation(t *testing.T) {
	valid := OAuthConfig{Origin: "https://example.com", DatabaseURL: "postgres://test", GoogleClientID: "id", GoogleClientSecret: "secret"}
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
	cfg.Grants = []Grant{{Provider: "google", Subject: "42", GoogleDomain: "example.com", Tenant: "x"}}
	if cfg.Validate() == nil {
		t.Fatal("ambiguous grant accepted")
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
	store.SaveSession(context.Background(), tokenHash(old), account{Provider: "google", Subject: "42"}, time.Now().Add(time.Hour))
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
	o.cfg.Grants = []Grant{{Provider: "google", GoogleDomain: "example.com", Tenant: "team"}}
	a, err := o.profile(context.Background(), "google", "https://provider.test/profile", "token")
	if err != nil {
		t.Fatal(err)
	}
	if a.GoogleDomain != "" || o.cfg.principal(a).Subject != "" {
		t.Fatal("unverified domain accepted")
	}
}

func TestLogoutRequiresCSRFHeaderAndOrigin(t *testing.T) {
	o, _, r := testOAuth(t, "google")
	for _, headers := range []map[string]string{{}, {"Origin": o.cfg.Origin}, {"X-Telemetry-CSRF": "1"}} {
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
			o.cfg.Grants = []Grant{{Provider: "google", GoogleEmail: "member@gmail.com", Tenant: "local", Permissions: []string{TracesRead}}}
			if err := o.cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			o.client.Transport = handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.profile))
			})}
			a, err := o.profile(context.Background(), "google", "https://provider.test/profile", "token")
			if err != nil {
				t.Fatal(err)
			}
			if got := o.cfg.principal(a); (got.Subject != "") != tc.allowed {
				t.Fatal("unexpected access", got)
			}
			token, _ := randomToken()
			store.SaveSession(context.Background(), tokenHash(token), a, time.Now().Add(time.Hour))
			request := httptest.NewRequest("GET", "/", nil)
			request.AddCookie(&http.Cookie{Name: o.cookieName("session"), Value: token})
			actor, err := o.Authenticate(request)
			if err != nil || (actor.Subject != "") != tc.allowed {
				t.Fatal("session lost email grant", actor, err)
			}
		})
	}
}
