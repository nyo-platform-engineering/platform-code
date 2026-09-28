package policy

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
)

// Policy is the complete access requirement for a registered HTTP endpoint.
// Public access must be explicit; protected endpoints always require tenant scope.
type Policy struct {
	Public     bool
	Permission string
}
type Endpoint struct{ Method, Path string }

// This table is the access-control audit surface. Route handlers live elsewhere.
var policies = map[Endpoint]Policy{
	{"GET", "/api/v1/auth/providers"}:          {Public: true},
	{"GET", "/api/v1/auth/session"}:            {Public: true},
	{"GET", "/api/v1/auth/:provider/login"}:    {Public: true},
	{"GET", "/api/v1/auth/:provider/callback"}: {Public: true},
	{"POST", "/api/v1/auth/logout"}:            {Public: true},
	{"GET", "/healthz"}:                        {Public: true},
	{"GET", "/readyz"}:                         {Public: true},
	{"GET", "/"}:                               {Public: true},
	{"GET", "/assets/*filepath"}:               {Public: true},
	{"GET", "/api/v1/meta"}:                    {Permission: auth.MetadataRead},
	{"GET", "/api/v1/data-sources"}:            {Permission: auth.MetadataRead},
	{"GET", "/api/v1/services"}:                {Permission: auth.MetadataRead},
	{"GET", "/api/v1/traces"}:                  {Permission: auth.TracesRead},
	{"GET", "/api/v1/traces/:traceId"}:         {Permission: auth.TracesRead},
	{"GET", "/api/v1/traces/red"}:              {Permission: auth.TracesRead},
	{"GET", "/api/v1/traces/attributes"}:       {Permission: auth.TracesRead},
	{"GET", "/api/v1/logs"}:                    {Permission: auth.LogsRead},
	{"GET", "/api/v1/logs/volume"}:             {Permission: auth.LogsRead},
	{"GET", "/api/v1/logs/attributes"}:         {Permission: auth.LogsRead},
	{"GET", "/api/v1/admin/summary"}:           {Permission: auth.AdminRead},
}

// ValidateRoutes prevents both unclassified routes and stale audit entries.
func ValidateRoutes(routes gin.RoutesInfo) error {
	seen := make(map[Endpoint]bool)
	for _, route := range routes {
		key := Endpoint{route.Method, route.Path}
		rule, ok := policies[key]
		if !ok || rule.Public == (rule.Permission != "") {
			return fmt.Errorf("missing or invalid access policy: %s %s", key.Method, key.Path)
		}
		seen[key] = true
	}
	for key := range policies {
		if !seen[key] {
			return fmt.Errorf("access policy has no route: %s %s", key.Method, key.Path)
		}
	}
	return nil
}

func Enforce(authenticator auth.Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only browser reads outside reserved namespaces may use the public SPA fallback.
		if c.FullPath() == "" {
			path := c.Request.URL.Path
			if path == "/api" || strings.HasPrefix(path, "/api/") || path == "/otlp" || strings.HasPrefix(path, "/otlp/") || (c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead) {
				c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_found"})
				return
			}
			c.Next()
			return
		}
		rule, ok := policies[Endpoint{c.Request.Method, c.FullPath()}]
		if !ok {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		if rule.Public {
			c.Next()
			return
		}
		var actor auth.Principal
		var err error
		if renewing, ok := authenticator.(auth.RenewingAuthenticator); ok {
			actor, err = renewing.AuthenticateAndRenew(c.Writer, c.Request)
		} else {
			actor, err = authenticator.Authenticate(c.Request)
		}
		if err != nil {
			c.AbortWithStatusJSON(503, gin.H{"error": "auth_unavailable"})
			return
		}
		if actor.Subject == "" {
			c.AbortWithStatusJSON(401, gin.H{"error": "unauthenticated"})
			return
		}
		if actor.Subject == "" || actor.Tenant == "" || !auth.HasPermission(actor, rule.Permission) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), actor))
		c.Next()
	}
}
