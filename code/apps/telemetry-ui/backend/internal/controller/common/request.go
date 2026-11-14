package common

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

var dataSourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func ParseRequest(c *gin.Context, signal model.Signal, operation model.Operation) (model.Request, bool) {
	signalScope := "span"
	if signal == model.SignalLogs {
		signalScope = "log"
	}
	f, err := parseFilter(c)
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid_query", "message": err.Error()})
		return model.Request{}, false
	}
	if operation == model.OperationAttributes {
		f.DiscoveryScope = c.DefaultQuery("scope", signalScope)
		f.KeySearch = c.Query("keySearch")
		if (f.DiscoveryScope != "resource" && f.DiscoveryScope != signalScope) ||
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
	if operation == model.OperationServices {
		f.Attributes = nil
	}
	dataSourceID := c.Query("dataSource")
	if dataSourceID != "" && !dataSourceIDPattern.MatchString(dataSourceID) {
		c.JSON(400, gin.H{"error": "invalid_query", "message": "Invalid datasource selection"})
		return model.Request{}, false
	}
	for _, attribute := range f.Attributes {
		logs := signal == model.SignalLogs
		if (logs && attribute.Scope == "span") || (!logs && (attribute.Scope == "log" || attribute.Scope == "body")) {
			c.JSON(400, gin.H{"error": "invalid_query", "message": "attribute scope does not match this signal"})
			return model.Request{}, false
		}
	}
	actor, ok := auth.FromContext(c.Request.Context())
	if !ok || actor.OrganizationScope == "" {
		c.JSON(403, gin.H{"error": "forbidden"})
		return model.Request{}, false
	}
	request := model.Request{Filter: f, Signal: signal, Operation: operation, DataSourceID: dataSourceID, OrganizationScope: actor.OrganizationScope}
	if err := request.Validate(); err != nil {
		c.JSON(400, gin.H{"error": "invalid_query", "message": err.Error()})
		return model.Request{}, false
	}

	return request, true
}
