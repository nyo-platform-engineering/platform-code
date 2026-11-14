package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"
	"gorm.io/gorm"
)

type organization struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	TelemetryScope string `json:"telemetryScope"`
}

func Organizations(control *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		db, actor, request, ok := listContext(c, control)
		if !ok {
			return
		}
		if db == nil {
			writePage(c, false, []organization{}, "")
			return
		}
		query := db.WithContext(c.Request.Context()).Model(&database.Organization{}).
			Where("id = ?", actor.OrganizationID)
		if request.Search != "" {
			pattern := searchPattern(request.Search)
			query = query.Where("(LOWER(id) LIKE ? ESCAPE '!' OR LOWER(name) LIKE ? ESCAPE '!' OR LOWER(telemetry_scope) LIKE ? ESCAPE '!')", pattern, pattern, pattern)
		}
		if request.After != "" {
			parts, valid := decodeCursor(request.After, 2)
			if !valid || parts[0] != request.Search {
				invalidQuery(c, "invalid cursor")
				return
			}
			query = query.Where("id > ?", parts[1])
		}
		rows := []database.Organization{}
		if err := query.Order("id").Limit(pageSize + 1).Find(&rows).Error; err != nil {
			unavailable(c)
			return
		}
		next := ""
		if len(rows) > pageSize {
			rows = rows[:pageSize]
			next = encodeCursor(request.Search, rows[len(rows)-1].ID)
		}
		result := make([]organization, len(rows))
		for i, row := range rows {
			result[i] = organization{ID: row.ID, Name: row.Name, TelemetryScope: row.TelemetryScope}
		}
		writePage(c, true, result, next)
	}
}
