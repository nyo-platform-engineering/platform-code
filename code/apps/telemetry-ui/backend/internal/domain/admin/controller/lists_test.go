package controller

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func adminDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&database.Organization{}, &database.AccessGrant{}, &database.AccessGrantPermission{}, &database.DataSource{}, &database.OrganizationDataSource{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]database.Organization{
		{ID: "org-a", Name: "Alpha", TelemetryScope: "alpha"},
		{ID: "org-b", Name: "Beta", TelemetryScope: "beta"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func adminRequest(t *testing.T, handler gin.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	out := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(out)
	c.Request = httptest.NewRequest("GET", path, nil)
	c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), auth.Principal{
		Subject: "admin", OrganizationID: "org-a", OrganizationScope: "alpha",
	}))
	handler(c)
	return out
}

func TestAdminListsStayOrganizationScoped(t *testing.T) {
	db := adminDatabase(t)
	for i := 0; i < 26; i++ {
		id := fmt.Sprintf("grant-%02d", i)
		grant := database.AccessGrant{ID: id, Provider: "google", SelectorType: "email", SelectorValue: id + "@example.com", OrganizationID: "org-a", ManagedBy: "database"}
		if err := db.Create(&grant).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&database.AccessGrant{ID: "other", Provider: "google", SelectorType: "email", SelectorValue: "other@example.com", OrganizationID: "org-b", ManagedBy: "database"}).Error; err != nil {
		t.Fatal(err)
	}

	first := adminRequest(t, AccessGrants(db), "/?q=GRANT")
	var firstPage page[accessGrant]
	if err := json.Unmarshal(first.Body.Bytes(), &firstPage); err != nil {
		t.Fatal(err)
	}
	if first.Code != 200 || len(firstPage.Data) != pageSize || firstPage.NextCursor == "" {
		t.Fatalf("unexpected first page: %d %s", first.Code, first.Body)
	}
	for _, grant := range firstPage.Data {
		if grant.ID == "other" {
			t.Fatal("grant from another organization leaked")
		}
		if grant.OrganizationID != "org-a" || grant.OrganizationName != "Alpha" {
			t.Fatalf("grant lost its organization: %#v", grant)
		}
	}

	next := adminRequest(t, AccessGrants(db), "/?q=grant&after="+firstPage.NextCursor)
	var nextPage page[accessGrant]
	if err := json.Unmarshal(next.Body.Bytes(), &nextPage); err != nil {
		t.Fatal(err)
	}
	if next.Code != 200 || len(nextPage.Data) != 1 || nextPage.Data[0].ID != "grant-25" || nextPage.NextCursor != "" {
		t.Fatalf("unexpected next page: %d %s", next.Code, next.Body)
	}
	mismatchedSearch := adminRequest(t, AccessGrants(db), "/?q=other&after="+firstPage.NextCursor)
	if mismatchedSearch.Code != 400 {
		t.Fatalf("cursor was accepted with another search: %d %s", mismatchedSearch.Code, mismatchedSearch.Body)
	}

	organizations := adminRequest(t, Organizations(db), "/?q=alpha")
	var organizationPage page[organization]
	if err := json.Unmarshal(organizations.Body.Bytes(), &organizationPage); err != nil {
		t.Fatal(err)
	}
	if len(organizationPage.Data) != 1 || organizationPage.Data[0].ID != "org-a" {
		t.Fatalf("organization scope lost: %s", organizations.Body)
	}
}

func TestAdminDataSourcesSearchAndCursorValidation(t *testing.T) {
	db := adminDatabase(t)
	sources := []database.DataSource{
		{ID: "archive", Address: "archive:9000", Database: "otel", Username: "reader", PasswordEnv: "CLICKHOUSE_ARCHIVE"},
		{ID: "primary", Address: "primary:9000", Database: "otel", Username: "reader", PasswordEnv: "CLICKHOUSE_PRIMARY"},
		{ID: "other", Address: "other:9000", Database: "otel", Username: "reader", PasswordEnv: "CLICKHOUSE_OTHER"},
	}
	if err := db.Create(&sources).Error; err != nil {
		t.Fatal(err)
	}
	assignments := []database.OrganizationDataSource{
		{OrganizationID: "org-a", Signal: "logs", DataSourceID: "archive"},
		{OrganizationID: "org-a", Signal: "traces", DataSourceID: "primary"},
		{OrganizationID: "org-b", Signal: "traces", DataSourceID: "other"},
	}
	if err := db.Create(&assignments).Error; err != nil {
		t.Fatal(err)
	}

	result := adminRequest(t, DataSources(db), "/?q=PRIMARY")
	var body page[dataSource]
	if err := json.Unmarshal(result.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if result.Code != 200 || len(body.Data) != 1 || body.Data[0].ID != "primary" || body.Data[0].Signal != "traces" || body.Data[0].OrganizationID != "org-a" || body.Data[0].OrganizationName != "Alpha" {
		t.Fatalf("unexpected datasource search: %d %s", result.Code, result.Body)
	}

	invalid := adminRequest(t, DataSources(db), "/?after=not-a-cursor")
	if invalid.Code != 400 {
		t.Fatalf("invalid cursor returned %d: %s", invalid.Code, invalid.Body)
	}
}
