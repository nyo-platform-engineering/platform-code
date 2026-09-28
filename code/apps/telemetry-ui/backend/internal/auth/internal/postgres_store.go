package authinternal

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/database"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type loginAttempt struct {
	Provider string
	Verifier string
	Expires  time.Time
}

type sessionState struct {
	Identity    providerIdentity
	FamilyHash  string
	Expires     time.Time
	LastSeen    time.Time
	IdleExpires time.Time
	RenewAfter  time.Time
	ReplacedAt  *time.Time
}

type sessionStore interface {
	SaveAttempt(context.Context, string, loginAttempt) error
	ConsumeAttempt(context.Context, string, string) (loginAttempt, error)
	SaveSession(context.Context, string, sessionState) error
	UseSession(context.Context, string, string, time.Time, time.Duration, time.Duration) (providerIdentity, bool, error)
	DeleteSession(context.Context, string) error
	Cleanup(context.Context) error
	Ping(context.Context) error
}

type principalResolver interface {
	ResolvePrincipal(context.Context, providerIdentity) (Principal, error)
}

type identitySelector struct {
	Type  string
	Value string
}

type postgresStore struct {
	db *gorm.DB
}

func NewOAuth(config OAuthConfig, db *gorm.DB) (*OAuth, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return newOAuth(config, &postgresStore{db: db}), nil
}

func (o *OAuth) principal(ctx context.Context, identity providerIdentity) (Principal, error) {
	if resolver, ok := o.store.(principalResolver); ok {
		return resolver.ResolvePrincipal(ctx, identity)
	}
	return o.config.principal(identity), nil
}

func (s *postgresStore) ResolvePrincipal(ctx context.Context, identity providerIdentity) (Principal, error) {
	for _, selector := range selectorsFor(identity) {
		if selector.Value == "" {
			continue
		}
		var grant database.AccessGrant
		err := s.db.WithContext(ctx).
			Where("provider = ? AND selector_type = ? AND selector_value = ?", identity.Provider, selector.Type, selector.Value).
			Take(&grant).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return Principal{}, err
		}
		return s.principalForGrant(ctx, identity, grant)
	}
	return Principal{}, nil
}

func selectorsFor(identity providerIdentity) []identitySelector {
	selectors := []identitySelector{{Type: "subject", Value: identity.Subject}}
	if identity.Provider == "google" {
		selectors = append(selectors,
			identitySelector{Type: "email", Value: identity.GoogleEmail},
			identitySelector{Type: "domain", Value: identity.GoogleDomain},
		)
	}
	return selectors
}

func (s *postgresStore) principalForGrant(ctx context.Context, identity providerIdentity, grant database.AccessGrant) (Principal, error) {
	var organization database.Organization
	if err := s.db.WithContext(ctx).Where("id = ?", grant.OrganizationID).Take(&organization).Error; err != nil {
		return Principal{}, err
	}
	var permissionRows []database.AccessGrantPermission
	if err := s.db.WithContext(ctx).Where("grant_id = ?", grant.ID).Find(&permissionRows).Error; err != nil {
		return Principal{}, err
	}
	permissions := make([]string, 0, len(permissionRows))
	for _, row := range permissionRows {
		permissions = append(permissions, row.Permission)
	}
	return Principal{
		Subject:           identity.Provider + ":" + identity.Subject,
		DisplayName:       identity.Name,
		OrganizationID:    organization.ID,
		OrganizationName:  organization.Name,
		OrganizationScope: organization.TelemetryScope,
		Tenant:            organization.TelemetryScope,
		Permissions:       permissions,
	}, nil
}

func (s *postgresStore) SaveAttempt(ctx context.Context, hash string, attempt loginAttempt) error {
	record := database.LoginAttempt{
		TokenHash: hash,
		Provider:  attempt.Provider,
		Verifier:  attempt.Verifier,
		ExpiresAt: attempt.Expires,
	}
	return s.db.WithContext(ctx).Create(&record).Error
}

func (s *postgresStore) ConsumeAttempt(ctx context.Context, hash, provider string) (loginAttempt, error) {
	var record database.LoginAttempt
	// Delete and return in one statement so concurrent callbacks cannot reuse state.
	result := s.db.WithContext(ctx).Clauses(clause.Returning{}).
		Where("token_hash = ? AND provider = ? AND expires_at > now()", hash, provider).
		Delete(&record)
	if result.Error != nil {
		return loginAttempt{}, result.Error
	}
	if result.RowsAffected == 0 {
		return loginAttempt{}, sql.ErrNoRows
	}
	return loginAttempt{
		Provider: record.Provider,
		Verifier: record.Verifier,
		Expires:  record.ExpiresAt,
	}, nil
}

func (s *postgresStore) SaveSession(ctx context.Context, hash string, state sessionState) error {
	record := database.Session{
		TokenHash:     hash,
		FamilyHash:    state.FamilyHash,
		Provider:      state.Identity.Provider,
		Subject:       state.Identity.Subject,
		DisplayName:   state.Identity.Name,
		GoogleDomain:  state.Identity.GoogleDomain,
		GoogleEmail:   state.Identity.GoogleEmail,
		ExpiresAt:     state.Expires,
		LastSeenAt:    state.LastSeen,
		IdleExpiresAt: state.IdleExpires,
		RenewAfter:    state.RenewAfter,
		ReplacedAt:    state.ReplacedAt,
	}
	return s.db.WithContext(ctx).Create(&record).Error
}

func (s *postgresStore) UseSession(ctx context.Context, hash, replacementHash string, now time.Time, idleTimeout, renewalInterval time.Duration) (identity providerIdentity, rotated bool, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record database.Session
		cutoff := now.Add(-renewalGracePeriod)
		result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("token_hash = ? AND expires_at > ? AND idle_expires_at > ? AND (replaced_at IS NULL OR replaced_at > ?)", hash, now, now, cutoff).
			Take(&record)
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return sql.ErrNoRows
		}
		if result.Error != nil {
			return result.Error
		}
		identity = providerIdentity{
			Provider: record.Provider, Subject: record.Subject, Name: record.DisplayName,
			GoogleDomain: record.GoogleDomain, GoogleEmail: record.GoogleEmail,
		}
		// A request already carrying the replaced identifier may finish during
		// the short grace window, but it cannot extend or rotate that session.
		if record.ReplacedAt != nil {
			return nil
		}
		idleExpires := now.Add(idleTimeout)
		if idleExpires.After(record.ExpiresAt) {
			idleExpires = record.ExpiresAt
		}
		if replacementHash == "" || now.Before(record.RenewAfter) {
			return tx.Model(&database.Session{}).Where("token_hash = ?", hash).
				Updates(map[string]any{"last_seen_at": now, "idle_expires_at": idleExpires}).Error
		}
		replacement := record
		replacement.TokenHash = replacementHash
		replacement.LastSeenAt = now
		replacement.IdleExpiresAt = idleExpires
		replacement.RenewAfter = now.Add(renewalInterval)
		replacement.ReplacedAt = nil
		if err := tx.Create(&replacement).Error; err != nil {
			return err
		}
		if err := tx.Model(&database.Session{}).Where("token_hash = ? AND replaced_at IS NULL", hash).
			Update("replaced_at", now).Error; err != nil {
			return err
		}
		rotated = true
		return nil
	})
	return identity, rotated, err
}

func (s *postgresStore) DeleteSession(ctx context.Context, hash string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record database.Session
		if err := tx.Select("family_hash").Where("token_hash = ?", hash).Take(&record).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		return tx.Where("family_hash = ?", record.FamilyHash).Delete(&database.Session{}).Error
	})
}

func (s *postgresStore) Cleanup(ctx context.Context) error {
	db := s.db.WithContext(ctx)
	if err := db.Where("expires_at <= now()").Delete(&database.LoginAttempt{}).Error; err != nil {
		return err
	}
	return db.Where("expires_at <= now() OR idle_expires_at <= now() OR replaced_at <= ?", time.Now().Add(-renewalGracePeriod)).Delete(&database.Session{}).Error
}

func (s *postgresStore) Ping(ctx context.Context) error {
	pool, err := s.db.DB()
	if err != nil {
		return err
	}
	return pool.PingContext(ctx)
}
