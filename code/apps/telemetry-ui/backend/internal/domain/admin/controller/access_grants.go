package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"
	"gorm.io/gorm"
)

type accessGrant struct {
	ID               string   `json:"id"`
	Provider         string   `json:"provider"`
	SelectorType     string   `json:"selectorType"`
	SelectorValue    string   `json:"selectorValue"`
	OrganizationID   string   `json:"organizationId"`
	OrganizationName string   `json:"organizationName"`
	ManagedBy        string   `json:"managedBy"`
	Permissions      []string `json:"permissions"`
}

func AccessGrants(control *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		db, actor, request, ok := listContext(c, control)
		if !ok {
			return
		}
		if db == nil {
			writePage(c, false, []accessGrant{}, "")
			return
		}
		query := db.WithContext(c.Request.Context()).Table("telemetry_access_grants AS access_grant").
			Select("access_grant.id, access_grant.provider, access_grant.selector_type, access_grant.selector_value, access_grant.organization_id, organization.name AS organization_name, access_grant.managed_by").
			Joins("JOIN telemetry_organizations AS organization ON organization.id = access_grant.organization_id").
			Where("access_grant.organization_id = ?", actor.OrganizationID)
		if request.Search != "" {
			pattern := searchPattern(request.Search)
			query = query.Where("(LOWER(access_grant.id) LIKE ? ESCAPE '!' OR LOWER(access_grant.provider) LIKE ? ESCAPE '!' OR LOWER(access_grant.selector_type) LIKE ? ESCAPE '!' OR LOWER(access_grant.selector_value) LIKE ? ESCAPE '!' OR LOWER(organization.name) LIKE ? ESCAPE '!')", pattern, pattern, pattern, pattern, pattern)
		}
		if request.After != "" {
			parts, valid := decodeCursor(request.After, 2)
			if !valid || parts[0] != request.Search {
				invalidQuery(c, "invalid cursor")
				return
			}
			query = query.Where("access_grant.id > ?", parts[1])
		}
		rows := []accessGrant{}
		if err := query.Order("access_grant.id").Limit(pageSize + 1).Scan(&rows).Error; err != nil {
			unavailable(c)
			return
		}
		next := ""
		if len(rows) > pageSize {
			rows = rows[:pageSize]
			next = encodeCursor(request.Search, rows[len(rows)-1].ID)
		}
		permissions := map[string][]string{}
		if len(rows) > 0 {
			ids := make([]string, len(rows))
			for i, row := range rows {
				ids[i] = row.ID
			}
			permissionRows := []database.AccessGrantPermission{}
			if err := db.WithContext(c.Request.Context()).Where("grant_id IN ?", ids).Order("grant_id, permission").Find(&permissionRows).Error; err != nil {
				unavailable(c)
				return
			}
			for _, row := range permissionRows {
				permissions[row.GrantID] = append(permissions[row.GrantID], row.Permission)
			}
		}
		for i := range rows {
			rows[i].Permissions = permissions[rows[i].ID]
			if rows[i].Permissions == nil {
				rows[i].Permissions = []string{}
			}
		}
		writePage(c, true, rows, next)
	}
}
