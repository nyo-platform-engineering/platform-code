package controller

// AI-assisted implementation.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"aat/aggregator/internal/model"
	"aat/internal/fieldreport"
	"aat/internal/httpkit"
	"aat/internal/telemetry"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrReportConflict = errors.New("idempotency key already used for another report")

func (ctrl *Controller) AcceptFieldReport(ctx context.Context, client, key string, raw []byte) (fieldreport.Receipt, error) {
	var receipt fieldreport.Receipt
	if client != "responder" || !fieldreport.ValidKey(key) {
		return receipt, Invalid{fmt.Errorf("invalid report client or idempotency key")}
	}
	report, err := fieldreport.Decode(raw)
	if err != nil {
		return receipt, Invalid{err}
	}
	_, hash, err := fieldreport.Canonical(report)
	if err != nil {
		return receipt, Invalid{err}
	}
	carrier := propagation.MapCarrier{}
	telemetry.Propagation.Inject(ctx, carrier)
	message := model.FieldReportSubmission{ID: fieldreport.ID(client, key), ClientID: client, PayloadHash: hash, Raw: raw,
		CorrelationID: httpkit.CorrelationID(ctx), TraceParent: carrier.Get("traceparent"), TraceState: carrier.Get("tracestate")}
	err = ctrl.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL synchronous_commit = 'on'").Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).Create(&message).Error; err != nil {
			return err
		}
		var stored model.FieldReportSubmission
		if err := tx.Select("id", "payload_hash", "accepted_at").Take(&stored, "id = ?", message.ID).Error; err != nil {
			return err
		}
		if stored.PayloadHash != hash {
			return ErrReportConflict
		}
		receipt = fieldreport.Receipt{ReportID: stored.ID, Status: "accepted", AcceptedAt: stored.AcceptedAt.UTC()}
		return nil
	})
	if err != nil {
		return fieldreport.Receipt{}, err
	}
	return receipt, nil
}

func (ctrl *Controller) FieldReportAccept(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.GetHeader("X-Client-ID") != "responder" {
		auditError(c, 403, "forbidden", "Tim Lapangan access required")
		return
	}
	key := c.GetHeader("Idempotency-Key")
	if !fieldreport.ValidKey(key) {
		auditError(c, 400, "bad_request", "invalid Idempotency-Key")
		return
	}
	raw, _, err := fieldreport.Read(c.Request.Body)
	if err != nil {
		status := 400
		if errors.Is(err, fieldreport.ErrTooLarge) {
			status = 413
		}
		auditError(c, status, "bad_request", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	ctx = httpkit.WithCorrelationID(ctx, c.GetHeader("X-Correlation-ID"))
	receipt, err := ctrl.AcceptFieldReport(ctx, "responder", key, raw)
	if err != nil {
		var invalid Invalid
		switch {
		case errors.Is(err, ErrReportConflict):
			auditError(c, 409, "idempotency_conflict", err.Error())
		case errors.As(err, &invalid):
			auditError(c, 400, "bad_request", invalid.Error())
		default:
			auditError(c, 503, "unavailable", "report storage unavailable; retry with the same key")
		}
		return
	}
	c.JSON(202, receipt)
}

// ProcessFieldReports locks each queued report until its result and status commit.
func (ctrl *Controller) ProcessFieldReports(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, Invalid{fmt.Errorf("invalid processing limit")}
	}
	processed := 0
	for range limit {
		found := false
		err := ctrl.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var message model.FieldReportSubmission
			result := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("processed_at IS NULL").Order("accepted_at, id").Limit(1).Find(&message)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return nil
			}
			found = true
			messageCtx := telemetry.Propagation.Extract(ctx, propagation.MapCarrier{"traceparent": message.TraceParent, "tracestate": message.TraceState})
			messageCtx = httpkit.WithCorrelationID(messageCtx, message.CorrelationID)
			messageCtx, span := telemetry.Start(messageCtx, "process field report", trace.SpanKindInternal)
			defer span.End()
			tx = tx.WithContext(messageCtx)
			report, err := fieldreport.Decode(message.Raw)
			if err != nil {
				telemetry.Fail(span, err)
				return err
			}
			document, hash, err := fieldreport.Canonical(report)
			if err != nil {
				return err
			}
			if hash != message.PayloadHash {
				return fmt.Errorf("stored report checksum mismatch")
			}
			var now time.Time
			if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
				return err
			}
			item := model.FieldReport{ReportID: message.ID, ClientID: message.ClientID, PayloadHash: hash, Document: document, AcceptedAt: message.AcceptedAt, ProcessedAt: now}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "report_id"}}, DoNothing: true}).Create(&item).Error; err != nil {
				return err
			}
			var stored model.FieldReport
			if err := tx.Select("payload_hash").Take(&stored, "report_id = ?", message.ID).Error; err != nil {
				return err
			}
			if stored.PayloadHash != hash {
				return fmt.Errorf("processed report checksum mismatch")
			}
			completion := tx.Model(&message).Updates(map[string]any{"processed_at": now, "raw": []byte{}})
			if completion.Error != nil {
				return completion.Error
			}
			if completion.RowsAffected != 1 {
				return fmt.Errorf("report completion was not stored")
			}
			return nil
		})
		if err != nil {
			return processed, err
		}
		if !found {
			break
		}
		processed++
	}
	return processed, nil
}

func (ctrl *Controller) FieldReportDetails(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	var message model.FieldReportSubmission
	if err := ctrl.DB.WithContext(ctx).Select("id", "accepted_at", "processed_at").Take(&message, "id = ?", c.Param("report_id")).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			auditError(c, 404, "not_found", "report not found")
		} else {
			auditError(c, 503, "unavailable", "report storage unavailable")
		}
		return
	}
	view := gin.H{"report_id": message.ID, "status": "accepted", "accepted_at": message.AcceptedAt.UTC()}
	if message.ProcessedAt != nil {
		var report model.FieldReport
		if err := ctrl.DB.WithContext(ctx).Take(&report, "report_id = ?", message.ID).Error; err != nil {
			auditError(c, 503, "unavailable", "report result unavailable")
			return
		}
		var document fieldreport.Submission
		if err := json.Unmarshal(report.Document, &document); err != nil {
			auditError(c, 503, "unavailable", "report result invalid")
			return
		}
		view["status"], view["processed_at"], view["report"] = "processed", report.ProcessedAt.UTC(), document
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(200, view)
}
