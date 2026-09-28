package controller

import (
	"encoding/base64"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	"gorm.io/gorm"
)

const pageSize = 25

type listRequest struct {
	Search string
	After  string
}

type page[T any] struct {
	DatabaseConfigured bool   `json:"databaseConfigured"`
	Data               []T    `json:"data"`
	NextCursor         string `json:"nextCursor,omitempty"`
}

func listContext(c *gin.Context, db *gorm.DB) (*gorm.DB, auth.Principal, listRequest, bool) {
	actor, ok := auth.FromContext(c.Request.Context())
	if !ok || actor.OrganizationID == "" {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil, auth.Principal{}, listRequest{}, false
	}
	searchValues, afterValues := c.QueryArray("q"), c.QueryArray("after")
	if len(searchValues) > 1 || len(afterValues) > 1 {
		invalidQuery(c, "q and after must be specified once")
		return nil, auth.Principal{}, listRequest{}, false
	}
	request := listRequest{Search: strings.ToLower(strings.TrimSpace(c.Query("q"))), After: c.Query("after")}
	if !utf8.ValidString(request.Search) || utf8.RuneCountInString(request.Search) > 128 || strings.ContainsFunc(request.Search, unicode.IsControl) {
		invalidQuery(c, "search must contain at most 128 characters without controls")
		return nil, auth.Principal{}, listRequest{}, false
	}
	if len(request.After) > 512 {
		invalidQuery(c, "invalid cursor")
		return nil, auth.Principal{}, listRequest{}, false
	}
	return db, actor, request, true
}

func searchPattern(value string) string {
	replacer := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return "%" + replacer.Replace(strings.ToLower(value)) + "%"
}

func encodeCursor(parts ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, "\x00")))
}

func decodeCursor(value string, parts int) ([]string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, false
	}
	decoded := strings.Split(string(raw), "\x00")
	return decoded, len(decoded) == parts
}

func writePage[T any](c *gin.Context, configured bool, rows []T, next string) {
	if rows == nil {
		rows = []T{}
	}
	c.JSON(http.StatusOK, page[T]{DatabaseConfigured: configured, Data: rows, NextCursor: next})
}

func invalidQuery(c *gin.Context, message string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_query", "message": message})
}

func unavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "control_plane_unavailable"})
}
