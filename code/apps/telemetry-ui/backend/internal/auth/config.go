package auth

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

type OAuthConfig struct {
	Origin                             string
	DatabaseURL                        string
	GoogleClientID, GoogleClientSecret string
	GitHubClientID, GitHubClientSecret string
	Grants                             []Grant
}

// Exactly one of Subject or GoogleDomain identifies an access grant.
type Grant struct {
	Provider     string   `json:"provider"`
	Subject      string   `json:"subject,omitempty"`
	GoogleDomain string   `json:"googleDomain,omitempty"`
	Tenant       string   `json:"tenant"`
	Permissions  []string `json:"permissions"`
}

type account struct {
	Provider     string `json:"provider"`
	Subject      string `json:"subject"`
	Name         string `json:"name"`
	GoogleDomain string `json:"googleDomain,omitempty"`
}

func LoadOAuthConfig() (OAuthConfig, error) {
	cfg := OAuthConfig{
		Origin:         strings.TrimRight(os.Getenv("AUTH_ORIGIN"), "/"),
		DatabaseURL:    os.Getenv("AUTH_DATABASE_URL"),
		GoogleClientID: os.Getenv("GOOGLE_CLIENT_ID"), GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		GitHubClientID: os.Getenv("GITHUB_CLIENT_ID"), GitHubClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
	}
	raw, err := os.ReadFile(os.Getenv("AUTH_GRANTS_FILE"))
	if err != nil {
		return cfg, fmt.Errorf("read AUTH_GRANTS_FILE: %w", err)
	}
	if err := json.Unmarshal(raw, &cfg.Grants); err != nil {
		return cfg, fmt.Errorf("invalid AUTH_GRANTS_FILE: %w", err)
	}
	return cfg, cfg.Validate()
}

func (c OAuthConfig) Validate() error {
	u, err := url.Parse(c.Origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("AUTH_ORIGIN must be an origin, such as https://telemetry.example.com")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return fmt.Errorf("AUTH_ORIGIN requires HTTPS except on loopback")
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("AUTH_DATABASE_URL is required")
	}
	if (c.GoogleClientID == "") != (c.GoogleClientSecret == "") || (c.GitHubClientID == "") != (c.GitHubClientSecret == "") {
		return fmt.Errorf("each OAuth provider needs both client ID and secret")
	}
	if c.GoogleClientID == "" && c.GitHubClientID == "" {
		return fmt.Errorf("configure at least one OAuth provider")
	}
	seen := map[string]bool{}
	for _, grant := range c.Grants {
		if grant.Provider != "google" && grant.Provider != "github" {
			return fmt.Errorf("invalid grant provider")
		}
		if (grant.Subject == "") == (grant.GoogleDomain == "") || (grant.GoogleDomain != "" && grant.Provider != "google") || strings.TrimSpace(grant.Tenant) == "" {
			return fmt.Errorf("each grant needs a tenant and either a subject or Google domain")
		}
		if grant.GoogleDomain != "" && (strings.ContainsAny(grant.GoogleDomain, " /:@*") || strings.ToLower(grant.GoogleDomain) != grant.GoogleDomain) {
			return fmt.Errorf("Google domain must be a lowercase hostname without wildcards")
		}
		key := grant.Provider + ":" + grant.Subject + ":" + grant.GoogleDomain
		if seen[key] {
			return fmt.Errorf("duplicate access grant")
		}
		seen[key] = true
		for _, permission := range grant.Permissions {
			if permission != MetadataRead && permission != TracesRead && permission != LogsRead {
				return fmt.Errorf("unknown grant permission %q", permission)
			}
		}
	}
	return nil
}

func (c OAuthConfig) principal(a account) Principal {
	// Specific accounts override domain-wide defaults. Never merge tenants.
	for _, bySubject := range []bool{true, false} {
		for _, grant := range c.Grants {
			if grant.Provider != a.Provider {
				continue
			}
			match := bySubject && grant.Subject != "" && grant.Subject == a.Subject
			match = match || (!bySubject && grant.GoogleDomain != "" && grant.GoogleDomain == a.GoogleDomain)
			if match {
				permissions := append([]string{MetadataRead}, grant.Permissions...)
				return Principal{Subject: a.Provider + ":" + a.Subject, DisplayName: a.Name, Tenant: grant.Tenant, Permissions: permissions}
			}
		}
	}
	return Principal{}
}
