package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BootstrapGrants imports the GitOps grant file into the PostgreSQL control
// plane. Database-managed grants are left untouched; config-managed rows are
// replaced so removals take effect at the next deployment.
func BootstrapGrants(ctx context.Context, db *gorm.DB, grants []Grant) error {
	organizationsByID := map[string]OrganizationConfig{}
	for _, grant := range grants {
		organizationsByID[grant.OrganizationID] = OrganizationConfig{
			ID: grant.OrganizationID, Name: firstNonEmpty(grant.OrganizationName, grant.OrganizationID),
			TelemetryScope: firstNonEmpty(grant.OrganizationScope, grant.OrganizationID),
		}
	}
	organizations := make([]OrganizationConfig, 0, len(organizationsByID))
	for _, organization := range organizationsByID {
		organizations = append(organizations, organization)
	}
	return BootstrapControlPlane(ctx, db, organizations, grants)
}

// BootstrapControlPlane imports organizations and their identity mappings.
func BootstrapControlPlane(ctx context.Context, db *gorm.DB, organizations []OrganizationConfig, grants []Grant) error {
	organizations = append([]OrganizationConfig(nil), organizations...)
	return database.WithControlLock(ctx, db, func(tx *gorm.DB) error {
		var old []database.AccessGrant
		if err := tx.Where("managed_by = ?", "config").Find(&old).Error; err != nil {
			return err
		}
		for _, grant := range old {
			if err := tx.Where("grant_id = ?", grant.ID).Delete(&database.AccessGrantPermission{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("managed_by = ?", "config").Delete(&database.AccessGrant{}).Error; err != nil {
			return err
		}

		sort.Slice(organizations, func(i, j int) bool { return organizations[i].ID < organizations[j].ID })
		for _, configured := range organizations {
			organization := database.Organization{ID: configured.ID, Name: configured.Name, TelemetryScope: configured.TelemetryScope}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{"name", "telemetry_scope"}),
			}).Create(&organization).Error; err != nil {
				return fmt.Errorf("bootstrap organization %q: %w", configured.ID, err)
			}
		}

		for _, configured := range grants {
			kind, value := grantSelector(configured)
			var existing database.AccessGrant
			err := tx.Where("provider = ? AND selector_type = ? AND selector_value = ?", configured.Provider, kind, value).Take(&existing).Error
			if err == nil {
				// A database-managed selector intentionally overrides bootstrap data.
				continue
			}
			if err != nil && err != gorm.ErrRecordNotFound {
				return err
			}
			grant := database.AccessGrant{
				ID:       grantID(configured.Provider, kind, value),
				Provider: configured.Provider, SelectorType: kind, SelectorValue: value,
				OrganizationID: configured.OrganizationID, ManagedBy: "config",
			}
			if err := tx.Create(&grant).Error; err != nil {
				return fmt.Errorf("bootstrap access grant: %w", err)
			}
			permissions := append([]string{MetadataRead}, configured.Permissions...)
			sort.Strings(permissions)
			for i, permission := range permissions {
				if i > 0 && permission == permissions[i-1] {
					continue
				}
				if err := tx.Create(&database.AccessGrantPermission{GrantID: grant.ID, Permission: permission}).Error; err != nil {
					return fmt.Errorf("bootstrap grant permission: %w", err)
				}
			}
		}
		return nil
	})
}

func grantID(provider, kind, value string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + kind + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

func grantSelector(grant Grant) (string, string) {
	switch {
	case grant.Subject != "":
		return "subject", grant.Subject
	case grant.GoogleEmail != "":
		return "email", grant.GoogleEmail
	default:
		return "domain", grant.GoogleDomain
	}
}
