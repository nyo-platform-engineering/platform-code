package controller

// AI-assisted implementation.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"aat/aggregator/internal/model"
	"aat/internal/httpkit"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var (
	ErrPollBusy   = errors.New("source already has a polling owner")
	ErrPollFenced = errors.New("polling ownership is no longer valid")
)

const PollSessionIdleTimeout = 20 * time.Second

// PollSession uses one connection so a disconnected owner cannot resume writing.
// Use it from one goroutine.
type PollSession struct {
	Controller *Controller
	Fence      model.PollOwner
	conn       *sql.Conn
}

func (session *PollSession) Close() {
	if session.conn != nil {
		// Close the connection to release its lock and keep it out of the pool.
		_ = session.conn.Raw(func(raw any) error {
			if pg, ok := raw.(*stdlib.Conn); ok {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = pg.Conn().Close(ctx)
			}
			return driver.ErrBadConn
		})
		_ = session.conn.Close()
		session.conn = nil
	}
}

func (ctrl *Controller) AcquirePollSession(ctx context.Context, source string) (*PollSession, error) {
	if source != "BMKG" && source != "PVMBG" {
		return nil, fmt.Errorf("unsupported polling source %q", source)
	}
	pool, err := ctrl.DB.DB()
	if err != nil {
		return nil, err
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		return nil, err
	}
	session := &PollSession{conn: conn}
	ok := false
	defer func() {
		if !ok {
			session.Close()
		}
	}()
	// Let PostgreSQL release ownership if the polling process freezes.
	for key, value := range map[string]string{
		"idle_session_timeout":                "20000ms",
		"idle_in_transaction_session_timeout": "5000ms",
		"statement_timeout":                   "5000ms",
	} {
		if _, err := conn.ExecContext(ctx, "SELECT set_config($1, $2, false)", key, value); err != nil {
			return nil, err
		}
	}
	var acquired bool
	// Include the schema so tests do not share locks with other schemas.
	err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(
		hashtextextended(current_schema() || ':aat:poll:' || $1, 403102))`, source).Scan(&acquired)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, ErrPollBusy
	}
	// PostgreSQL 17+ can also limit total transaction time.
	var version int
	if err := conn.QueryRowContext(ctx, "SHOW server_version_num").Scan(&version); err != nil {
		return nil, err
	}
	if version >= 170000 {
		if _, err := conn.ExecContext(ctx, "SELECT set_config('transaction_timeout', '10000ms', false)"); err != nil {
			return nil, err
		}
	}
	var fence model.PollOwner
	err = conn.QueryRowContext(ctx, `INSERT INTO poll_owners (source, epoch, owner, backend_pid)
		VALUES ($1, 1, $2, pg_backend_pid())
		ON CONFLICT (source) DO UPDATE SET epoch = poll_owners.epoch + 1,
		owner = excluded.owner, backend_pid = excluded.backend_pid
		RETURNING source, epoch, owner, backend_pid`, source, httpkit.ID()).
		Scan(&fence.Source, &fence.Epoch, &fence.Owner, &fence.BackendPID)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: conn}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               ctrl.DB.Logger,
	})
	if err != nil {
		return nil, err
	}
	owned := *ctrl
	owned.DB = db
	owned.PollOwner = &fence
	session.Controller, session.Fence = &owned, fence
	ok = true
	return session, nil
}

func (ctrl *Controller) ValidatePollOwner(ctx context.Context, source string) error {
	if ctrl.PollOwner == nil {
		return ErrPollFenced
	}
	return ctrl.fencePoll(ctrl.DB.WithContext(ctx), source, false)
}

func (ctrl *Controller) fencePoll(tx *gorm.DB, source string, lock bool) error {
	fence := ctrl.PollOwner
	if fence == nil || fence.Source != source {
		return ErrPollFenced
	}
	query := `SELECT epoch FROM poll_owners WHERE source = ? AND epoch = ?
		AND owner = ? AND backend_pid = pg_backend_pid()`
	if lock {
		query += " FOR UPDATE"
	}
	var epoch int64
	result := tx.Raw(query, source, fence.Epoch, fence.Owner).Scan(&epoch)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 || epoch != fence.Epoch {
		return ErrPollFenced
	}
	return nil
}
