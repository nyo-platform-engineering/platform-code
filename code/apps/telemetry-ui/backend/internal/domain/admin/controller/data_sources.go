package controller

import (
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type dataSource struct {
	ID                  string `json:"id"`
	Signal              string `json:"signal"`
	OrganizationID      string `json:"organizationId"`
	OrganizationName    string `json:"organizationName"`
	Address             string `json:"address"`
	Database            string `json:"database"`
	Username            string `json:"username"`
	Secure              bool   `json:"secure"`
	ManagedBy           string `json:"managedBy"`
	AssignmentManagedBy string `json:"assignmentManagedBy"`
}

func DataSources(control *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		db, actor, request, ok := listContext(c, control)
		if !ok {
			return
		}
		if db == nil {
			writePage(c, false, []dataSource{}, "")
			return
		}
		query := db.WithContext(c.Request.Context()).Table("telemetry_organization_data_sources AS assignment").
			Select("source.id, assignment.signal, assignment.organization_id, organization.name AS organization_name, source.address, source.database, source.username, source.secure, source.managed_by, assignment.managed_by AS assignment_managed_by").
			Joins("JOIN telemetry_data_sources AS source ON source.id = assignment.data_source_id").
			Joins("JOIN telemetry_organizations AS organization ON organization.id = assignment.organization_id").
			Where("assignment.organization_id = ?", actor.OrganizationID)
		if request.Search != "" {
			pattern := searchPattern(request.Search)
			query = query.Where("(LOWER(source.id) LIKE ? ESCAPE '!' OR LOWER(assignment.signal) LIKE ? ESCAPE '!' OR LOWER(organization.name) LIKE ? ESCAPE '!' OR LOWER(source.address) LIKE ? ESCAPE '!' OR LOWER(source.database) LIKE ? ESCAPE '!' OR LOWER(source.username) LIKE ? ESCAPE '!')", pattern, pattern, pattern, pattern, pattern, pattern)
		}
		if request.After != "" {
			parts, valid := decodeCursor(request.After, 3)
			if !valid || parts[0] != request.Search || (parts[1] != "traces" && parts[1] != "logs") {
				invalidQuery(c, "invalid cursor")
				return
			}
			query = query.Where("(assignment.signal > ? OR (assignment.signal = ? AND assignment.data_source_id > ?))", parts[1], parts[1], parts[2])
		}
		rows := []dataSource{}
		if err := query.Order("assignment.signal, assignment.data_source_id").Limit(pageSize + 1).Scan(&rows).Error; err != nil {
			unavailable(c)
			return
		}
		next := ""
		if len(rows) > pageSize {
			rows = rows[:pageSize]
			last := rows[len(rows)-1]
			next = encodeCursor(request.Search, last.Signal, last.ID)
		}
		writePage(c, true, rows, next)
	}
}
