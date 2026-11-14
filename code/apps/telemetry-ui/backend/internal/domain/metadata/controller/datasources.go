package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"
	"gorm.io/gorm"
)

type dataSourceOption struct {
	ID        string `json:"id"`
	Address   string `json:"address"`
	Database  string `json:"database"`
	Secure    bool   `json:"secure"`
	ManagedBy string `json:"managedBy"`
}

type assignedDataSource struct {
	database.DataSource
	Signal string
}

// DataSources lists only datasource choices assigned to the actor's active
// organization. Connection credentials and their environment references stay server-side.
func DataSources(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		actor, ok := auth.FromContext(c.Request.Context())
		if !ok || actor.OrganizationID == "" {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		if db == nil {
			fallback := []dataSourceOption{{ID: "default", Address: "Configured environment", ManagedBy: "config"}}
			c.JSON(http.StatusOK, gin.H{"traces": fallback, "logs": fallback})
			return
		}

		rows := []assignedDataSource{}
		if err := db.WithContext(c.Request.Context()).Table(database.DataSource{}.TableName()+" AS source").
			Select("source.*, assignment.signal").
			Joins("JOIN telemetry_organization_data_sources AS assignment ON assignment.data_source_id = source.id").
			Where("assignment.organization_id = ?", actor.OrganizationID).
			Order("assignment.signal, source.id").Scan(&rows).Error; err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "control_plane_unavailable"})
			return
		}
		result := map[string][]dataSourceOption{"traces": {}, "logs": {}}
		for _, row := range rows {
			result[row.Signal] = append(result[row.Signal], dataSourceOption{
				ID: row.ID, Address: row.Address, Database: row.Database,
				Secure: row.Secure, ManagedBy: row.ManagedBy,
			})
		}
		c.JSON(http.StatusOK, result)
	}
}
