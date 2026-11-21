package controller

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"log/slog"
	"time"

	"aat/internal/httpkit"
	"github.com/jackc/pgx/v5/pgconn"
)

type pollWriteEvidence struct {
	Epoch         int64
	Owner         string
	BackendPID    int `gorm:"column:backend_pid"`
	DBObservedAt  time.Time
	TransactionID string
}

func pollEvidenceCorrelation(ctx context.Context) string {
	if id := httpkit.CorrelationID(ctx); id != "" {
		return id
	}
	return httpkit.ID()
}

func pollRejectionReason(err error) string {
	if errors.Is(err, ErrPollFenced) {
		return "fence_mismatch"
	}
	var pg *pgconn.PgError
	if errors.Is(err, sql.ErrConnDone) || errors.Is(err, driver.ErrBadConn) ||
		(errors.As(err, &pg) && (pg.Code == "57P01" || pg.Code == "57P05")) {
		return "session_unavailable"
	}
	return "database_operation_failed"
}

func (ctrl *Controller) logPollRejection(ctx context.Context, source, operation string, err error) {
	if ctrl.PollOwner == nil {
		return
	}
	slog.Warn("poll storage rejected", "source", source, "operation", operation,
		"epoch", ctrl.PollOwner.Epoch, "owner", ctrl.PollOwner.Owner,
		"backend_pid", ctrl.PollOwner.BackendPID,
		"correlation_id", pollEvidenceCorrelation(ctx), "reason", pollRejectionReason(err))
}

func (ctrl *Controller) logPollWrite(ctx context.Context, source, operation string, proof pollWriteEvidence, err error) {
	if err != nil {
		ctrl.logPollRejection(ctx, source, operation, err)
		return
	}
	// Emit only after COMMIT succeeds; DBObservedAt is the fence-check time.
	slog.Info("poll storage committed", "source", source, "operation", operation,
		"epoch", proof.Epoch, "owner", proof.Owner, "backend_pid", proof.BackendPID,
		"db_observed_at", proof.DBObservedAt.UTC().Format(time.RFC3339Nano),
		"transaction_id", proof.TransactionID, "correlation_id", pollEvidenceCorrelation(ctx))
}
