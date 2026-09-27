package common

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func parseFilter(c *gin.Context) (model.Filter, error) {
	f := model.Filter{To: time.Now().UTC(), Environment: c.DefaultQuery("environment", "local"), TraceID: c.Query("traceId"), Limit: 100}
	if len(c.Request.URL.Query()["q"]) > 1 {
		return f, errors.New("q must be specified once")
	}
	f.Search = strings.TrimSpace(c.Query("q"))
	size := utf8.RuneCountInString(f.Search)
	if !utf8.ValidString(f.Search) || size > 256 || (size > 0 && size < 3) || strings.ContainsFunc(f.Search, unicode.IsControl) {
		return f, errors.New("search must contain 3–256 characters without control characters")
	}
	var err error
	f.Attributes, err = model.ParseAttributes(c.Request.URL.Query()["attr"])
	if err != nil {
		return f, err
	}
	if value := c.Query("to"); value != "" {
		f.To, err = time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return f, errors.New("invalid to timestamp")
		}
	}
	f.From = f.To.Add(-30 * time.Minute)
	if value := c.Query("from"); value != "" {
		f.From, err = time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return f, errors.New("invalid from timestamp")
		}
	}
	if !f.From.Before(f.To) || f.To.Sub(f.From) > 24*time.Hour {
		return f, errors.New("range must be positive and at most 24 hours")
	}
	if pathID := c.Param("traceId"); pathID != "" {
		f.TraceID = pathID
	}
	if f.TraceID != "" && (!model.TraceIDPattern.MatchString(f.TraceID) || f.TraceID == strings.Repeat("0", 32)) {
		return f, errors.New("invalid trace ID")
	}
	for _, filter := range []struct {
		key    string
		target *[]string
		max    int
	}{{"service", &f.Services, 20}, {"severity", &f.Severities, 7}, {"status", &f.Statuses, 2}} {
		values := c.Request.URL.Query()[filter.key]
		if len(values) > filter.max {
			return f, fmt.Errorf("too many %s values (maximum %d)", filter.key, filter.max)
		}
		seen := map[string]bool{}
		for _, value := range values {
			if value == "" {
				if len(values) > 1 {
					return f, fmt.Errorf("empty %s cannot be combined with other values", filter.key)
				}
				continue
			}
			if len(value) > 128 {
				return f, errors.New("filter too long")
			}
			if filter.key == "severity" {
				if _, ok := model.SeverityRanges[value]; !ok {
					return f, errors.New("invalid severity")
				}
			}
			if filter.key == "status" && value != "error" && value != "ok" {
				return f, errors.New("invalid status")
			}
			if !seen[value] {
				*filter.target = append(*filter.target, value)
				seen[value] = true
			}
		}
	}
	if len(f.Environment) > 128 {
		return f, errors.New("filter too long")
	}
	if value := c.Query("limit"); value != "" {
		f.Limit, err = strconv.Atoi(value)
		if err != nil || f.Limit < 1 || f.Limit > 500 {
			return f, errors.New("limit must be 1–500")
		}
	}
	if value := c.Query("offset"); value != "" {
		f.Offset, err = strconv.Atoi(value)
		if err != nil || f.Offset < 0 || f.Offset > 5000 {
			return f, errors.New("offset must be 0–5000")
		}
	}
	if value := c.Query("minDurationMs"); value != "" {
		f.MinDuration, err = strconv.ParseFloat(value, 64)
		if err != nil || f.MinDuration < 0 || f.MinDuration > 86400000 || strings.ContainsAny(value, "NaInf") {
			return f, errors.New("invalid minimum duration")
		}
	}
	return f, nil
}
