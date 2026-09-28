package auth

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"
)

const (
	defaultSessionLifetime = 8 * time.Hour
	minSessionLifetime     = 15 * time.Minute
	maxSessionLifetime     = 12 * time.Hour
	defaultIdleTimeout     = 30 * time.Minute
	minIdleTimeout         = 5 * time.Minute
	defaultRenewalInterval = 15 * time.Minute
	minRenewalInterval     = 5 * time.Minute
)

type OAuthConfig struct {
	Origin             string
	DatabaseURL        string
	GoogleClientID     string
	GoogleClientSecret string
	GitHubClientID     string
	GitHubClientSecret string
	SessionLifetime    time.Duration
	IdleTimeout        time.Duration
	RenewalInterval    time.Duration
	Organizations      []OrganizationConfig
	Grants             []Grant
}

func LoadOAuthConfig() (OAuthConfig, error) {
	config := OAuthConfig{
		Origin:             strings.TrimRight(os.Getenv("AUTH_ORIGIN"), "/"),
		DatabaseURL:        database.URLFromEnv(),
		SessionLifetime:    defaultSessionLifetime,
		IdleTimeout:        defaultIdleTimeout,
		RenewalInterval:    defaultRenewalInterval,
		GoogleClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		GoogleClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		GitHubClientID:     os.Getenv("GITHUB_CLIENT_ID"),
		GitHubClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
	}
	var err error
	if config.SessionLifetime, err = durationFromEnv("AUTH_SESSION_LIFETIME", config.SessionLifetime, "8h"); err != nil {
		return config, err
	}
	if config.IdleTimeout, err = durationFromEnv("AUTH_IDLE_TIMEOUT", config.IdleTimeout, "30m"); err != nil {
		return config, err
	}
	if config.RenewalInterval, err = durationFromEnv("AUTH_RENEWAL_INTERVAL", config.RenewalInterval, "15m"); err != nil {
		return config, err
	}
	if err := loadAccessConfiguration(&config); err != nil {
		return config, err
	}
	return config, config.Validate()
}

func durationFromEnv(name string, fallback time.Duration, example string) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration such as %s", name, example)
	}
	return value, nil
}

func (c OAuthConfig) Validate() error {
	if err := c.validateEndpoints(); err != nil {
		return err
	}
	if err := c.validateSessionTiming(); err != nil {
		return err
	}
	if err := c.validateProviders(); err != nil {
		return err
	}
	organizationIDs, err := validateOrganizations(c.Organizations)
	if err != nil {
		return err
	}
	return validateGrants(c.Grants, organizationIDs)
}

func (c OAuthConfig) validateEndpoints() error {
	origin, err := url.Parse(c.Origin)
	if err != nil || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return fmt.Errorf("AUTH_ORIGIN must be an origin, such as https://telemetry.example.com")
	}
	if origin.Scheme != "https" && !(origin.Scheme == "http" && isLoopbackHost(origin.Hostname())) {
		return fmt.Errorf("AUTH_ORIGIN requires HTTPS except on loopback")
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	databaseURL, err := url.Parse(c.DatabaseURL)
	if err != nil || (databaseURL.Scheme != "postgres" && databaseURL.Scheme != "postgresql") || databaseURL.Host == "" {
		return fmt.Errorf("DATABASE_URL must be a PostgreSQL URL")
	}
	if origin.Scheme == "https" && databaseURL.Query().Get("sslmode") != "verify-full" {
		return fmt.Errorf("DATABASE_URL must use sslmode=verify-full when AUTH_ORIGIN uses HTTPS")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (c OAuthConfig) validateSessionTiming() error {
	if c.sessionLifetime() < minSessionLifetime || c.sessionLifetime() > maxSessionLifetime {
		return fmt.Errorf("AUTH_SESSION_LIFETIME must be between %s and %s", minSessionLifetime, maxSessionLifetime)
	}
	if c.idleTimeout() < minIdleTimeout || c.idleTimeout() > c.sessionLifetime() {
		return fmt.Errorf("AUTH_IDLE_TIMEOUT must be between %s and AUTH_SESSION_LIFETIME", minIdleTimeout)
	}
	if c.renewalInterval() < minRenewalInterval || c.renewalInterval() >= c.idleTimeout() {
		return fmt.Errorf("AUTH_RENEWAL_INTERVAL must be between %s and less than AUTH_IDLE_TIMEOUT", minRenewalInterval)
	}
	return nil
}

func (c OAuthConfig) validateProviders() error {
	if (c.GoogleClientID == "") != (c.GoogleClientSecret == "") || (c.GitHubClientID == "") != (c.GitHubClientSecret == "") {
		return fmt.Errorf("each OAuth provider needs both client ID and secret")
	}
	if c.GoogleClientID == "" && c.GitHubClientID == "" {
		return fmt.Errorf("configure at least one OAuth provider")
	}
	return nil
}

func (c OAuthConfig) sessionLifetime() time.Duration {
	if c.SessionLifetime == 0 {
		return defaultSessionLifetime
	}
	return c.SessionLifetime
}

func (c OAuthConfig) idleTimeout() time.Duration {
	if c.IdleTimeout == 0 {
		return defaultIdleTimeout
	}
	return c.IdleTimeout
}

func (c OAuthConfig) renewalInterval() time.Duration {
	if c.RenewalInterval == 0 {
		return defaultRenewalInterval
	}
	return c.RenewalInterval
}
