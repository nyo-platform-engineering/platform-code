package query

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRoutedPoolRequiresSecretEnvironmentReference(t *testing.T) {
	store := &RoutedStore{signal: "traces", pools: map[string]*clickHouseStore{}, mu: &sync.Mutex{}}
	base := database.DataSource{ID: "test", Address: "clickhouse:9000", Database: "otel", Username: "reader"}

	invalid := base
	invalid.PasswordEnv = "PASSWORD"
	if _, err := store.pool(invalid); err == nil {
		t.Fatal("accepted datasource credential outside CLICKHOUSE_ namespace")
	}
	missing := base
	missing.PasswordEnv = "CLICKHOUSE_MISSING_TEST_PASSWORD"
	if _, err := store.pool(missing); err == nil {
		t.Fatal("accepted missing datasource credential")
	}

	t.Setenv("CLICKHOUSE_ROUTED_TEST_PASSWORD", "secret")
	valid := base
	valid.PasswordEnv = "CLICKHOUSE_ROUTED_TEST_PASSWORD"
	first, err := store.pool(valid)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.pool(valid)
	if err != nil || first != second {
		t.Fatal("identical datasource did not reuse its pool", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUniqueStrings(t *testing.T) {
	values := []string{"a", "a", "b", "b", "c"}
	got := uniqueStrings(values)
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatal(got)
	}
}

func TestCatalogAllowsMultipleDataSourcesPerSignal(t *testing.T) {
	raw := `{
  "dataSources": {
    "unified": [{"id":"primary","address":"primary:9000","database":"otel","username":"reader","passwordEnv":"CLICKHOUSE_PRIMARY_PASSWORD"}],
    "traces": [{"id":"archive","address":"archive:9000","database":"otel","username":"reader","passwordEnv":"CLICKHOUSE_ARCHIVE_PASSWORD","secure":true}],
    "logs": []
  },
  "assignments": {
    "unified": [],
    "traces": [{"organizationId":"acme","dataSourceIds":["primary","archive"]}],
    "logs": [{"organizationId":"acme","dataSourceIds":["primary"]}]
  }
}`
	catalog, err := parseDataSourceCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Assignments) != 2 || len(catalog.Assignments[0].DataSourceIDs) != 2 || catalog.DataSources[0].ManagedBy != "config" {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
	invalidSignal := strings.Replace(raw, `"logs": [{"organizationId":"acme","dataSourceIds":["primary"]}]`, `"logs": [{"organizationId":"acme","dataSourceIds":["archive"]}]`, 1)
	if _, err := parseDataSourceCatalog(invalidSignal); err == nil {
		t.Fatal("logs assignment accepted a trace-only datasource")
	}
	defaults, err := parseDataSourceCatalog(`{
  "dataSources":{"unified":[{"id":"default","address":"db:9000","database":"otel","username":"reader","passwordEnv":"CLICKHOUSE_DEFAULT_PASSWORD"}],"traces":[],"logs":[]},
  "assignments":{"unified":[],"traces":[],"logs":[]}
}`)
	if err != nil || len(defaults.Assignments) != 0 || len(defaults.DefaultDataSourceIDs) != 1 || defaults.DefaultDataSourceIDs[0] != "default" {
		t.Fatalf("implicit unified assignment config failed: %#v %v", defaults, err)
	}
}

func TestRoutedStoreRequiresSelectionWhenSignalHasMultipleSources(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:routed-selection?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&database.DataSource{}, &database.OrganizationDataSource{}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLICKHOUSE_PRIMARY_PASSWORD", "primary-secret")
	t.Setenv("CLICKHOUSE_ARCHIVE_PASSWORD", "archive-secret")
	sources := []database.DataSource{
		{ID: "primary", Address: "primary:9000", Database: "otel", Username: "reader", PasswordEnv: "CLICKHOUSE_PRIMARY_PASSWORD"},
		{ID: "archive", Address: "archive:9000", Database: "otel", Username: "reader", PasswordEnv: "CLICKHOUSE_ARCHIVE_PASSWORD"},
	}
	if err := db.Create(&sources).Error; err != nil {
		t.Fatal(err)
	}
	assignments := []database.OrganizationDataSource{
		{OrganizationID: "acme", Signal: "traces", DataSourceID: "primary"},
		{OrganizationID: "acme", Signal: "traces", DataSourceID: "archive"},
	}
	if err := db.Create(&assignments).Error; err != nil {
		t.Fatal(err)
	}
	store := &RoutedStore{control: db, signal: "traces", pools: map[string]*clickHouseStore{}, mu: &sync.Mutex{}}
	if _, err := store.resolve(context.Background(), "acme", ""); err == nil {
		t.Fatal("ambiguous datasource routing was accepted")
	}
	selected, err := store.resolve(context.Background(), "acme", "archive")
	if err != nil || selected == nil {
		t.Fatal("assigned datasource selection failed", err)
	}
	if _, err := store.resolve(context.Background(), "acme", "not-assigned"); err == nil {
		t.Fatal("unassigned datasource selection was accepted")
	}
	_ = store.Close()
}
