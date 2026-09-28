package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type provider struct {
	config     oauth2.Config
	profileURL string
}

func configuredProviders(cfg OAuthConfig) map[string]provider {
	providers := make(map[string]provider)
	if cfg.GoogleClientID != "" {
		providers["google"] = provider{oauth2.Config{
			ClientID: cfg.GoogleClientID, ClientSecret: cfg.GoogleClientSecret,
			RedirectURL: cfg.Origin + "/api/v1/auth/google/callback",
			Scopes:      []string{"openid", "profile", "email"},
			Endpoint:    oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", AuthStyle: oauth2.AuthStyleInParams},
		}, "https://openidconnect.googleapis.com/v1/userinfo"}
	}
	if cfg.GitHubClientID != "" {
		providers["github"] = provider{oauth2.Config{
			ClientID: cfg.GitHubClientID, ClientSecret: cfg.GitHubClientSecret,
			RedirectURL: cfg.Origin + "/api/v1/auth/github/callback",
			Endpoint:    oauth2.Endpoint{AuthURL: "https://github.com/login/oauth/authorize", TokenURL: "https://github.com/login/oauth/access_token", AuthStyle: oauth2.AuthStyleInParams},
		}, "https://api.github.com/user"}
	}
	return providers
}

func (o *OAuth) Providers(c *gin.Context) {
	names := make([]string, 0, len(o.providers))
	for name := range o.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	c.JSON(200, gin.H{"mode": "oauth", "providers": names})
}

func (o *OAuth) profile(ctx context.Context, name, endpoint, accessToken string) (account, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return account{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return account{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return account{}, fmt.Errorf("provider profile unavailable")
	}
	var profile struct {
		Sub           string `json:"sub"`
		ID            int64  `json:"id"`
		Login         string `json:"login"`
		Name          string `json:"name"`
		Domain        string `json:"hd"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&profile); err != nil {
		return account{}, err
	}
	a := account{Provider: name, Subject: profile.Sub, Name: profile.Name}
	if name == "github" {
		if profile.ID <= 0 {
			return account{}, fmt.Errorf("missing provider identity")
		}
		a.Subject = strconv.FormatInt(profile.ID, 10)
		if a.Name == "" {
			a.Name = profile.Login
		}
	} else if name == "google" && profile.EmailVerified {
		a.GoogleEmail = strings.ToLower(profile.Email)
		a.GoogleDomain = strings.ToLower(profile.Domain)
	}
	if a.Subject == "" {
		return account{}, fmt.Errorf("missing provider identity")
	}
	if a.Name == "" {
		a.Name = a.Subject
	}
	return a, nil
}
