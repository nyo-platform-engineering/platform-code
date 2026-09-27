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
	{"GET", "/healthz"}:                  {Public: true},
	{"GET", "/readyz"}:                   {Public: true},
	{"GET", "/"}:                         {Public: true},
	{"GET", "/assets/*filepath"}:         {Public: true},
	{"GET", "/api/v1/meta"}:              {Permission: auth.MetadataRead},
	{"GET", "/api/v1/services"}:          {Permission: auth.MetadataRead},
	{"GET", "/api/v1/traces"}:            {Permission: auth.TracesRead},
	{"GET", "/api/v1/traces/:traceId"}:   {Permission: auth.TracesRead},
	{"GET", "/api/v1/traces/red"}:        {Permission: auth.TracesRead},
	{"GET", "/api/v1/traces/attributes"}: {Permission: auth.TracesRead},
	{"GET", "/api/v1/logs"}:              {Permission: auth.LogsRead},
	{"GET", "/api/v1/logs/volume"}:       {Permission: auth.LogsRead},
	{"GET", "/api/v1/logs/attributes"}:   {Permission: auth.LogsRead},
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
		actor := authenticator.Authenticate(c.Request)
		if actor.Subject == "" || actor.Tenant == "" || !auth.HasPermission(actor, rule.Permission) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), actor))
		c.Next()
	}
}
