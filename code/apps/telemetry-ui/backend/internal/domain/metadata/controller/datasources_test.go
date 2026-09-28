package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestDataSourcesAreOrganizationScopedAndDoNotExposeCredentialReferences(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:datasource-catalog?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&database.DataSource{}, &database.OrganizationDataSource{}); err != nil {
		t.Fatal(err)
	}
	sources := []database.DataSource{
		{ID: "acme-primary", Address: "acme:9000", Database: "otel", Username: "reader", PasswordEnv: "CLICKHOUSE_ACME_SECRET"},
		{ID: "other", Address: "other:9000", Database: "otel", Username: "reader", PasswordEnv: "CLICKHOUSE_OTHER_SECRET"},
	}
	if err := db.Create(&sources).Error; err != nil {
		t.Fatal(err)
	}
	assignments := []database.OrganizationDataSource{
		{OrganizationID: "acme", Signal: "traces", DataSourceID: "acme-primary"},
		{OrganizationID: "other-org", Signal: "traces", DataSourceID: "other"},
	}
	if err := db.Create(&assignments).Error; err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		principal := auth.Principal{Subject: "reader", OrganizationID: "acme", OrganizationScope: "acme"}
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
		c.Next()
	})
	router.GET("/data-sources", DataSources(db))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/data-sources", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), "CLICKHOUSE_") || strings.Contains(response.Body.String(), "other:9000") {
		t.Fatalf("catalog leaked credential metadata or another organization: %s", response.Body)
	}
	var body struct {
		Traces []dataSourceOption `json:"traces"`
		Logs   []dataSourceOption `json:"logs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Traces) != 1 || body.Traces[0].ID != "acme-primary" || len(body.Logs) != 0 {
		t.Fatalf("unexpected scoped catalog: %#v", body)
	}
}
