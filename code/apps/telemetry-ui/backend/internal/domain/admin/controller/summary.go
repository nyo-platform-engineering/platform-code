package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"
	"gorm.io/gorm"
)

type dataSource struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	Database  string `json:"database"`
	Username  string `json:"username"`
	Secure    bool   `json:"secure"`
	ManagedBy string `json:"managedBy"`
}

type organization struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	TelemetryScope string `json:"telemetryScope"`
}

type assignment struct {
	OrganizationID string `json:"organizationId"`
	Signal         string `json:"signal"`
	DataSourceID   string `json:"dataSourceId"`
	ManagedBy      string `json:"managedBy"`
}

// Summary returns non-secret control-plane inventory. Policy requires the
// observability:admin:read permission before this handler runs.
func Summary(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		actor, ok := auth.FromContext(ctx)
		if !ok || actor.OrganizationID == "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		if db == nil {
			c.JSON(http.StatusOK, gin.H{
				"databaseConfigured": false,
				"organizations":      []organization{},
				"dataSources":        []dataSource{},
				"assignments":        []assignment{},
				"grantCount":         0,
			})
			return
		}

		organizations := []database.Organization{}
		if err := db.WithContext(ctx).Where("id = ?", actor.OrganizationID).Find(&organizations).Error; err != nil {
			unavailable(c)
			return
		}
		publicOrganizations := make([]organization, len(organizations))
		for i, item := range organizations {
			publicOrganizations[i] = organization{
				ID: item.ID, Name: item.Name, TelemetryScope: item.TelemetryScope,
			}
		}

		sources := []database.DataSource{}
		if err := db.WithContext(ctx).
			Distinct("telemetry_data_sources.*").
			Joins("JOIN telemetry_organization_data_sources assignment ON assignment.data_source_id = telemetry_data_sources.id").
			Where("assignment.organization_id = ?", actor.OrganizationID).
			Order("telemetry_data_sources.id").Find(&sources).Error; err != nil {
			unavailable(c)
			return
		}
		publicSources := make([]dataSource, len(sources))
		for i, source := range sources {
			publicSources[i] = dataSource{
				ID: source.ID, Address: source.Address, Database: source.Database,
				Username: source.Username, Secure: source.Secure, ManagedBy: source.ManagedBy,
			}
		}

		rows := []database.OrganizationDataSource{}
		if err := db.WithContext(ctx).Where("organization_id = ?", actor.OrganizationID).Order("signal, data_source_id").Find(&rows).Error; err != nil {
			unavailable(c)
			return
		}
		assignments := make([]assignment, len(rows))
		for i, row := range rows {
			assignments[i] = assignment{
				OrganizationID: row.OrganizationID, Signal: row.Signal,
				DataSourceID: row.DataSourceID, ManagedBy: row.ManagedBy,
			}
		}

		var grantCount int64
		if err := db.WithContext(ctx).Model(&database.AccessGrant{}).Where("organization_id = ?", actor.OrganizationID).Count(&grantCount).Error; err != nil {
			unavailable(c)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"databaseConfigured": true,
			"organizations":      publicOrganizations,
			"dataSources":        publicSources,
			"assignments":        assignments,
			"grantCount":         grantCount,
		})
	}
}

func unavailable(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "control_plane_unavailable"})
}
