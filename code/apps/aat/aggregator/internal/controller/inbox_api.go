package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInboxProcessed = errors.New("message is already processed")

type UnprocessedItem struct {
	MessageID  string    `json:"message_id"`
	Source     string    `json:"source"`
	ReceivedAt time.Time `json:"received_at"`
	Reason     string    `json:"reason"`
	Retryable  bool      `json:"retryable"`
	Attempts   int       `json:"attempts"`
	Status     string    `json:"status"`
}

type UnprocessedPage struct {
	Items      []UnprocessedItem `json:"items"`
	NextCursor *string           `json:"next_cursor"`
}

type inboxCursor struct {
	Version int    `json:"v"`
	Source  string `json:"source"`
	After   int64  `json:"after"`
}

func (ctrl *Controller) ListUnprocessed(ctx context.Context, source string, limit int, cursor string) (UnprocessedPage, error) {
	page := UnprocessedPage{Items: []UnprocessedItem{}}
	if limit < 1 || limit > 1000 || source != "" && !model.ValidSource(source) {
		return page, Invalid{fmt.Errorf("invalid source or limit")}
	}
	position := inboxCursor{Version: 1, Source: source}
	if cursor != "" {
		if len(cursor) > 4096 {
			return page, storage.ErrInvalidCursor
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(raw, &position) != nil || position.Version != 1 || position.Source != source || position.After < 1 {
			return page, storage.ErrInvalidCursor
		}
	}
	query := ctrl.DB.WithContext(ctx).Where("status <> 'processed' AND sequence > ?", position.After)
	if source != "" {
		query = query.Where("source = ?", source)
	}
	var messages []model.InboxMessage
	if err := query.Select("sequence", "message_id", "source", "received_at", "reason", "attempts", "status").Order("sequence").Limit(limit + 1).Find(&messages).Error; err != nil {
		return page, err
	}
	if len(messages) > limit {
		position.After = messages[limit-1].Sequence
		raw, _ := json.Marshal(position)
		next := base64.RawURLEncoding.EncodeToString(raw)
		page.NextCursor = &next
		messages = messages[:limit]
	}
	for _, message := range messages {
		page.Items = append(page.Items, UnprocessedItem{message.MessageID, message.Source, message.ReceivedAt.UTC(), message.Reason, message.Status == "retrying", message.Attempts, message.Status})
	}
	return page, nil
}

func (ctrl *Controller) AuditUnprocessed(c *gin.Context) {
	q, ok := auditQuery(c)
	if !ok {
		return
	}
	if c.Request.URL.Query().Has("since") {
		auditError(c, 400, "bad_request", "since is not supported")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	page, err := ctrl.ListUnprocessed(ctx, q.Source, q.Limit, q.Cursor)
	if err != nil {
		auditFailure(c, err)
		return
	}
	c.JSON(200, page)
}

func (ctrl *Controller) InboxDetail(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	var message model.InboxMessage
	if err := ctrl.DB.WithContext(ctx).Take(&message, "message_id = ?", c.Param("message_id")).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			auditError(c, 404, "not_found", "message not found")
		} else {
			auditFailure(c, err)
		}
		return
	}
	message.ReceivedAt = message.ReceivedAt.UTC()
	c.JSON(200, message)
}

func (ctrl *Controller) ReplayInbox(ctx context.Context, id string) error {
	return ctrl.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var message model.InboxMessage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Take(&message, "message_id = ?", id).Error; err != nil {
			return err
		}
		if message.Status == "processed" {
			return ErrInboxProcessed
		}
		return tx.Model(&message).Updates(map[string]any{"status": "retrying", "reason": "replay_requested", "next_attempt_at": gorm.Expr("clock_timestamp()")}).Error
	})
}

func (ctrl *Controller) InboxReplay(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if err := ctrl.ReplayInbox(ctx, c.Param("message_id")); err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			auditError(c, 404, "not_found", "message not found")
		case errors.Is(err, ErrInboxProcessed):
			auditError(c, 409, "already_processed", err.Error())
		default:
			auditFailure(c, err)
		}
		return
	}
	c.JSON(200, gin.H{"message_id": c.Param("message_id"), "status": "retrying"})
}
