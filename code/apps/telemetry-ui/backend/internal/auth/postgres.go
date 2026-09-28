package auth

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

type sessionStore interface {
	SaveAttempt(context.Context, string, loginAttempt) error
	ConsumeAttempt(context.Context, string, string) (loginAttempt, error)
	SaveSession(context.Context, string, account, time.Time) error
	Session(context.Context, string) (account, error)
	DeleteSession(context.Context, string) error
	Cleanup(context.Context) error
	Ping(context.Context) error
}

type postgresStore struct {
	db *gorm.DB
}

func NewOAuth(cfg OAuthConfig, db *gorm.DB) (*OAuth, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return newOAuth(cfg, &postgresStore{db: db}), nil
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

func (s *postgresStore) SaveSession(ctx context.Context, hash string, identity account, expires time.Time) error {
	record := database.Session{
		TokenHash:    hash,
		Provider:     identity.Provider,
		Subject:      identity.Subject,
		DisplayName:  identity.Name,
		GoogleDomain: identity.GoogleDomain,
		ExpiresAt:    expires,
	}
	return s.db.WithContext(ctx).Create(&record).Error
}

func (s *postgresStore) Session(ctx context.Context, hash string) (account, error) {
	var record database.Session
	err := s.db.WithContext(ctx).
		Where("token_hash = ? AND expires_at > now()", hash).
		Take(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return account{}, sql.ErrNoRows
	}
	if err != nil {
		return account{}, err
	}
	return account{
		Provider:     record.Provider,
		Subject:      record.Subject,
		Name:         record.DisplayName,
		GoogleDomain: record.GoogleDomain,
	}, nil
}

func (s *postgresStore) DeleteSession(ctx context.Context, hash string) error {
	return s.db.WithContext(ctx).Where("token_hash = ?", hash).Delete(&database.Session{}).Error
}

func (s *postgresStore) Cleanup(ctx context.Context) error {
	db := s.db.WithContext(ctx)
	if err := db.Where("expires_at <= now()").Delete(&database.LoginAttempt{}).Error; err != nil {
		return err
	}
	return db.Where("expires_at <= now()").Delete(&database.Session{}).Error
}

func (s *postgresStore) Ping(ctx context.Context) error {
	pool, err := s.db.DB()
	if err != nil {
		return err
	}
	return pool.PingContext(ctx)
}
