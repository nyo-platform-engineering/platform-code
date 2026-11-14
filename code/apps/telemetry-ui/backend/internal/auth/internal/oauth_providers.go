package authinternal

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

type oauthProvider struct {
	config     oauth2.Config
	profileURL string
}

func configuredProviders(config OAuthConfig) map[string]oauthProvider {
	providers := make(map[string]oauthProvider)
	if config.GoogleClientID != "" {
		providers["google"] = oauthProvider{oauth2.Config{
			ClientID: config.GoogleClientID, ClientSecret: config.GoogleClientSecret,
			RedirectURL: config.Origin + "/api/v1/auth/google/callback",
			Scopes:      []string{"openid", "profile", "email"},
			Endpoint:    oauth2.Endpoint{AuthURL: "https://accounts.google.com/o/oauth2/v2/auth", TokenURL: "https://oauth2.googleapis.com/token", AuthStyle: oauth2.AuthStyleInParams},
		}, "https://openidconnect.googleapis.com/v1/userinfo"}
	}
	if config.GitHubClientID != "" {
		providers["github"] = oauthProvider{oauth2.Config{
			ClientID: config.GitHubClientID, ClientSecret: config.GitHubClientSecret,
			RedirectURL: config.Origin + "/api/v1/auth/github/callback",
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
	c.JSON(http.StatusOK, gin.H{"mode": "oauth", "providers": names})
}

func (o *OAuth) fetchIdentity(ctx context.Context, providerName, endpoint, accessToken string) (providerIdentity, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return providerIdentity{}, err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Accept", "application/json")
	response, err := o.client.Do(request)
	if err != nil {
		return providerIdentity{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return providerIdentity{}, fmt.Errorf("provider profile unavailable")
	}
	var claims struct {
		Sub           string `json:"sub"`
		ID            int64  `json:"id"`
		Login         string `json:"login"`
		Name          string `json:"name"`
		Domain        string `json:"hd"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&claims); err != nil {
		return providerIdentity{}, err
	}
	identity := providerIdentity{Provider: providerName, Subject: claims.Sub, Name: claims.Name}
	if providerName == "github" {
		if claims.ID <= 0 {
			return providerIdentity{}, fmt.Errorf("missing provider identity")
		}
		identity.Subject = strconv.FormatInt(claims.ID, 10)
		if identity.Name == "" {
			identity.Name = claims.Login
		}
	} else if providerName == "google" && claims.EmailVerified {
		identity.GoogleEmail = strings.ToLower(claims.Email)
		identity.GoogleDomain = strings.ToLower(claims.Domain)
	}
	if identity.Subject == "" {
		return providerIdentity{}, fmt.Errorf("missing provider identity")
	}
	if identity.Name == "" {
		identity.Name = identity.Subject
	}
	return identity, nil
}
