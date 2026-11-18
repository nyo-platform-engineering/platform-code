package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"aat/internal/fieldreport"
	"github.com/gin-gonic/gin"
)

func (u upstream) fieldReports(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || parts[0] != "Bearer" || len(parts[1]) > 256 {
		unauthorized(c)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), u.config.Timeout)
	defer cancel()
	var identity struct {
		Active bool   `json:"active"`
		ID     string `json:"client_id"`
	}
	correlation := c.GetHeader("X-Correlation-ID")
	if err := u.introspectInto(ctx, parts[1], correlation, &identity); err != nil {
		unavailable(c)
		return
	}
	if !identity.Active || !identityKnown(identity.ID) {
		unauthorized(c)
		return
	}
	if identity.ID != "responder" {
		c.JSON(403, gin.H{"error": "Tim Lapangan access required"})
		return
	}
	key := c.GetHeader("Idempotency-Key")
	if !fieldreport.ValidKey(key) {
		c.JSON(400, gin.H{"error": "invalid Idempotency-Key"})
		return
	}
	raw, _, err := fieldreport.Read(c.Request.Body)
	if err != nil {
		status := 400
		if errors.Is(err, fieldreport.ErrTooLarge) {
			status = 413
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	receipt, err := u.submitFieldReport(ctx, raw, key, correlation)
	if err != nil {
		var upstreamError upstreamStatusError
		if errors.As(err, &upstreamError) && (upstreamError.Status == 400 || upstreamError.Status == 409 || upstreamError.Status == 413) {
			message := "invalid field report"
			if upstreamError.Status == 409 {
				message = "idempotency key already used for another report"
			}
			c.JSON(upstreamError.Status, gin.H{"error": message})
			return
		}
		c.JSON(503, gin.H{"error": "report acceptance unavailable; retry with the same key"})
		return
	}
	c.JSON(202, receipt)
}

func (u upstream) submitFieldReport(ctx context.Context, raw []byte, key, correlation string) (fieldreport.Receipt, error) {
	var receipt fieldreport.Receipt
	request, err := http.NewRequestWithContext(ctx, "POST", u.endpoint(u.config.AggregatorURL, "/internal/field-reports"), bytes.NewReader(raw))
	if err != nil {
		return receipt, err
	}
	request.Header.Set("Authorization", "Bearer "+u.config.AggregatorToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Correlation-ID", correlation)
	request.Header.Set("X-Client-ID", "responder")
	request.Header.Set("Idempotency-Key", key)
	response, err := u.http.Do(request)
	if err != nil {
		return receipt, err
	}
	defer response.Body.Close()
	if response.StatusCode != 202 {
		return receipt, upstreamStatusError{Status: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return receipt, err
	}
	if len(body) > 4096 || json.Unmarshal(body, &receipt) != nil || receipt.ReportID != fieldreport.ID("responder", key) || receipt.Status != "accepted" || receipt.AcceptedAt.IsZero() {
		return fieldreport.Receipt{}, fmt.Errorf("invalid acceptance receipt")
	}
	receipt.AcceptedAt = receipt.AcceptedAt.UTC()
	return receipt, nil
}
