package authinternal

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"os"
	"strings"
)

type OrganizationConfig struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	TelemetryScope   string            `json:"telemetryScope"`
	IdentityMappings []IdentityMapping `json:"identityMappings"`
}

type IdentityMapping struct {
	Provider    string        `json:"provider"`
	Match       IdentityMatch `json:"match"`
	Permissions []string      `json:"permissions"`
}

type IdentityMatch struct {
	Subject string `json:"subject,omitempty"`
	Email   string `json:"email,omitempty"`
	Domain  string `json:"domain,omitempty"`
}

// Grant is the normalized identity-to-organization rule used by both supported
// access-configuration formats.
type Grant struct {
	Provider          string   `json:"provider"`
	Subject           string   `json:"subject,omitempty"`
	GoogleEmail       string   `json:"googleEmail,omitempty"`
	GoogleDomain      string   `json:"googleDomain,omitempty"`
	OrganizationID    string   `json:"organizationId"`
	Permissions       []string `json:"permissions"`
	OrganizationName  string   `json:"-"`
	OrganizationScope string   `json:"-"`
}

type providerIdentity struct {
	Provider     string `json:"provider"`
	Subject      string `json:"subject"`
	Name         string `json:"name"`
	GoogleDomain string `json:"googleDomain,omitempty"`
	GoogleEmail  string `json:"googleEmail,omitempty"`
}

func loadAccessConfiguration(config *OAuthConfig) error {
	configFile := os.Getenv("AUTH_ORGANIZATIONS_FILE")
	usesLegacyGrants := configFile == ""
	if usesLegacyGrants {
		configFile = os.Getenv("AUTH_GRANTS_FILE")
	}
	if configFile == "" {
		return fmt.Errorf("AUTH_ORGANIZATIONS_FILE or AUTH_GRANTS_FILE is required")
	}
	raw, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("read authorization config: %w", err)
	}
	if usesLegacyGrants {
		if err := json.Unmarshal(raw, &config.Grants); err != nil {
			return fmt.Errorf("invalid AUTH_GRANTS_FILE: %w", err)
		}
		return nil
	}
	if err := json.Unmarshal(raw, &config.Organizations); err != nil {
		return fmt.Errorf("invalid AUTH_ORGANIZATIONS_FILE: %w", err)
	}
	config.Grants = grantsFromOrganizations(config.Organizations)
	return nil
}

func grantsFromOrganizations(organizations []OrganizationConfig) []Grant {
	var grants []Grant
	for _, organization := range organizations {
		for _, mapping := range organization.IdentityMappings {
			grants = append(grants, Grant{
				Provider:          mapping.Provider,
				Subject:           mapping.Match.Subject,
				GoogleEmail:       mapping.Match.Email,
				GoogleDomain:      mapping.Match.Domain,
				OrganizationID:    organization.ID,
				Permissions:       mapping.Permissions,
				OrganizationName:  organization.Name,
				OrganizationScope: organization.TelemetryScope,
			})
		}
	}
	return grants
}

func validateOrganizations(organizations []OrganizationConfig) (map[string]bool, error) {
	organizationIDs := map[string]bool{}
	organizationScopes := map[string]bool{}
	for _, organization := range organizations {
		if strings.TrimSpace(organization.ID) == "" || strings.TrimSpace(organization.Name) == "" || strings.TrimSpace(organization.TelemetryScope) == "" {
			return nil, fmt.Errorf("each organization needs id, name, and telemetryScope")
		}
		if organizationIDs[organization.ID] || organizationScopes[organization.TelemetryScope] {
			return nil, fmt.Errorf("organization ids and telemetry scopes must be unique")
		}
		organizationIDs[organization.ID] = true
		organizationScopes[organization.TelemetryScope] = true
	}
	return organizationIDs, nil
}

func validateGrants(grants []Grant, organizationIDs map[string]bool) error {
	seenSelectors := map[string]bool{}
	for _, grant := range grants {
		if grant.Provider != "google" && grant.Provider != "github" {
			return fmt.Errorf("invalid grant provider")
		}
		if grant.selectorCount() != 1 || strings.TrimSpace(grant.OrganizationID) == "" {
			return fmt.Errorf("each grant needs an organization ID and exactly one subject, Google email, or Google domain")
		}
		if len(organizationIDs) > 0 && !organizationIDs[grant.OrganizationID] {
			return fmt.Errorf("identity mapping references unknown organization %q", grant.OrganizationID)
		}
		if (grant.GoogleEmail != "" || grant.GoogleDomain != "") && grant.Provider != "google" {
			return fmt.Errorf("Google email and domain grants require the Google provider")
		}
		if grant.GoogleEmail != "" {
			address, err := mail.ParseAddress(grant.GoogleEmail)
			if err != nil || address.Address != grant.GoogleEmail || strings.ToLower(grant.GoogleEmail) != grant.GoogleEmail {
				return fmt.Errorf("Google email must be an exact lowercase email address")
			}
		}
		if grant.GoogleDomain != "" && (strings.ContainsAny(grant.GoogleDomain, " /:@*") || strings.ToLower(grant.GoogleDomain) != grant.GoogleDomain) {
			return fmt.Errorf("Google domain must be a lowercase hostname without wildcards")
		}
		selectorKey := grant.Provider + ":" + grant.Subject + ":" + grant.GoogleEmail + ":" + grant.GoogleDomain
		if seenSelectors[selectorKey] {
			return fmt.Errorf("duplicate access grant")
		}
		seenSelectors[selectorKey] = true
		for _, permission := range grant.Permissions {
			if !isKnownPermission(permission) {
				return fmt.Errorf("unknown grant permission %q", permission)
			}
		}
	}
	return nil
}

func (g Grant) selectorCount() int {
	count := 0
	for _, value := range []string{g.Subject, g.GoogleEmail, g.GoogleDomain} {
		if value != "" {
			count++
		}
	}
	return count
}

func isKnownPermission(permission string) bool {
	switch permission {
	case MetadataRead, TracesRead, LogsRead, AdminRead:
		return true
	default:
		return false
	}
}

func (c OAuthConfig) principal(identity providerIdentity) Principal {
	// Prefer stable subject grants, then exact emails, then domain grants.
	for _, selectorType := range []string{"subject", "email", "domain"} {
		for _, grant := range c.Grants {
			if grant.Provider != identity.Provider || !grant.matches(identity, selectorType) {
				continue
			}
			organizationScope := firstNonEmpty(grant.OrganizationScope, grant.OrganizationID)
			return Principal{
				Subject:           identity.Provider + ":" + identity.Subject,
				DisplayName:       identity.Name,
				OrganizationID:    grant.OrganizationID,
				OrganizationName:  firstNonEmpty(grant.OrganizationName, grant.OrganizationID),
				OrganizationScope: organizationScope,
				Permissions:       append([]string{MetadataRead}, grant.Permissions...),
			}
		}
	}
	return Principal{}
}

func (g Grant) matches(identity providerIdentity, selectorType string) bool {
	switch selectorType {
	case "subject":
		return g.Subject != "" && g.Subject == identity.Subject
	case "email":
		return identity.Provider == "google" && g.GoogleEmail != "" && g.GoogleEmail == identity.GoogleEmail
	case "domain":
		return identity.Provider == "google" && g.GoogleDomain != "" && g.GoogleDomain == identity.GoogleDomain
	default:
		return false
	}
}

func firstNonEmpty(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
