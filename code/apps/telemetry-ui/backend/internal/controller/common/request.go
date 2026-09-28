package common

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func ParseRequest(c *gin.Context, kind string) (model.Request, bool) {
	signal := "span"
	if kind == "logs" || kind == "logs-volume" || kind == "logs-keys" {
		signal = "log"
	}
	f, err := parseFilter(c)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid_query", "message": err.Error()})
		return model.Request{}, false
	}
	if kind == "logs-keys" || kind == "traces-keys" {
		f.DiscoveryScope = c.DefaultQuery("scope", signal)
		f.KeySearch = c.Query("keySearch")
		if (f.DiscoveryScope != "resource" && f.DiscoveryScope != signal) ||
			!utf8.ValidString(f.KeySearch) ||
			utf8.RuneCountInString(f.KeySearch) > 128 ||
			strings.ContainsFunc(f.KeySearch, unicode.IsControl) {
			c.JSON(400, gin.H{"error": "invalid_query", "message": "Invalid attribute scope or key search"})
			return model.Request{}, false
		}
		f.Attributes = nil
		f.Search = ""
		f.Limit = 50
		f.Offset = 0
	}
	if kind == "services" {
		f.Attributes = nil
	}
	for _, attribute := range f.Attributes {
		logs := signal == "log"
		if (logs && attribute.Scope == "span") || (!logs && (attribute.Scope == "log" || attribute.Scope == "body")) {
			c.JSON(400, gin.H{"error": "invalid_query", "message": "attribute scope does not match this signal"})
			return model.Request{}, false
		}
	}
	actor, ok := auth.FromContext(c.Request.Context())
	if !ok || actor.Tenant == "" {
		c.JSON(403, gin.H{"error": "forbidden"})
		return model.Request{}, false
	}
	request := model.Request{Filter: f, Kind: kind, Tenant: actor.Tenant}
	if err := request.Validate(); err != nil {
		c.JSON(400, gin.H{"error": "invalid_query", "message": err.Error()})
		return model.Request{}, false
	}

	return request, true
}
