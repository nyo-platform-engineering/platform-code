package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/gin-gonic/gin"
)

type queryStore interface {
	Query(context.Context, string, ...any) ([]map[string]any, error)
}
type clickHouseStore struct{ db *sql.DB }

func openStore() *clickHouseStore {
	db := clickhouse.OpenDB(&clickhouse.Options{
		Addr:        []string{envOr("CLICKHOUSE_ADDR", "127.0.0.1:9000")},
		Auth:        clickhouse.Auth{Database: "otel", Username: envOr("CLICKHOUSE_USER", "app"), Password: envOr("CLICKHOUSE_PASSWORD", "local-clickhouse-app")},
		DialTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second,
	})
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	return &clickHouseStore{db}
}
func (s *clickHouseStore) Query(ctx context.Context, query string, args ...any) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, col := range columns {
			row[col] = values[i]
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type queryFilter struct {
	Attributes                     []attributeFilter
	DiscoveryScope, KeySearch      string
	From, To                       time.Time
	Environment, TraceID, Search   string
	Services, Severities, Statuses []string
	Limit, Offset                  int
	MinDuration                    float64
}

var traceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var severityRanges = map[string][2]int{"unspecified": {0, 0}, "trace": {1, 4}, "debug": {5, 8}, "info": {9, 12}, "warn": {13, 16}, "error": {17, 20}, "fatal": {21, 24}}

func parseFilter(c *gin.Context) (queryFilter, error) {
	f := queryFilter{To: time.Now().UTC(), Environment: c.DefaultQuery("environment", "local"), TraceID: c.Query("traceId"), Limit: 100}
	if len(c.Request.URL.Query()["q"]) > 1 {
		return f, errors.New("q must be specified once")
	}
	f.Search = strings.TrimSpace(c.Query("q"))
	size := utf8.RuneCountInString(f.Search)
	if !utf8.ValidString(f.Search) || size > 256 || (size > 0 && size < 3) || strings.ContainsFunc(f.Search, unicode.IsControl) {
		return f, errors.New("search must contain 3–256 characters without control characters")
	}
	var err error
	f.Attributes, err = parseAttributes(c.Request.URL.Query()["attr"])
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
	if f.TraceID != "" && (!traceIDPattern.MatchString(f.TraceID) || f.TraceID == strings.Repeat("0", 32)) {
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
				if _, ok := severityRanges[value]; !ok {
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
func (f queryFilter) where(tenant string, logs bool) (string, []any) {
	clause := "Timestamp >= fromUnixTimestamp64Nano(?) AND Timestamp < fromUnixTimestamp64Nano(?) AND ResourceAttributes['tenant.id'] = ?"
	args := []any{f.From.UnixNano(), f.To.UnixNano(), tenant}
	for _, item := range []struct{ column, value string }{{"ResourceAttributes['deployment.environment.name']", f.Environment}, {"TraceId", f.TraceID}} {
		if item.value != "" {
			clause += " AND " + item.column + " = ?"
			args = append(args, item.value)
		}
	}
	if len(f.Services) > 0 {
		placeholders := make([]string, len(f.Services))
		for i, service := range f.Services {
			placeholders[i] = "?"
			args = append(args, service)
		}
		clause += " AND ServiceName IN (" + strings.Join(placeholders, ", ") + ")"
	}
	if logs && len(f.Severities) > 0 {
		bands := make([]string, len(f.Severities))
		for i, severity := range f.Severities {
			band := severityRanges[severity]
			bands[i] = "SeverityNumber BETWEEN ? AND ?"
			args = append(args, band[0], band[1])
		}
		clause += " AND (" + strings.Join(bands, " OR ") + ")"
	}
	if !logs {
		if len(f.Statuses) == 1 {
			if f.Statuses[0] == "error" {
				clause += " AND StatusCode = 'Error'"
			} else {
				clause += " AND StatusCode != 'Error'"
			}
		}
		if f.MinDuration > 0 {
			clause += " AND Duration >= ?"
			args = append(args, f.MinDuration*1e6)
		}
	}
	if f.Search != "" {
		if traceIDPattern.MatchString(strings.ToLower(f.Search)) {
			clause += " AND TraceId = ?"
			args = append(args, strings.ToLower(f.Search))
		} else {
			column := "SpanName"
			if logs {
				column = "Body"
			}
			clause += " AND (positionCaseInsensitiveUTF8(" + column + ", ?) > 0 OR positionCaseInsensitiveUTF8(ServiceName, ?) > 0 OR positionCaseInsensitiveUTF8(TraceId, ?) > 0)"
			args = append(args, f.Search, f.Search, f.Search)
		}
	}
	for _, attribute := range f.Attributes {
		predicate, values := attribute.sql()
		clause += " AND " + predicate
		args = append(args, values...)
	}
	return clause, args
}

// Fail instead of returning incomplete counts when a search exceeds its read budget.
const searchSettings = " SETTINGS max_execution_time=3, max_rows_to_read=2000000, max_bytes_to_read=268435456, read_overflow_mode='throw'"

func (f queryFilter) settings() string {
	if f.Search != "" || len(f.Attributes) > 0 {
		return searchSettings
	}
	return ""
}

const redFields = "count() AS requests, countIf(StatusCode = 'Error') AS errors, if(count() = 0, 0, countIf(StatusCode = 'Error') / count()) AS errorRate, if(count()=0,NULL,quantilesTDigest(0.50,0.90,0.95,0.99)(Duration)[1]/1000000) AS p50Ms, if(count()=0,NULL,quantilesTDigest(0.50,0.90,0.95,0.99)(Duration)[2]/1000000) AS p90Ms, if(count()=0,NULL,quantilesTDigest(0.50,0.90,0.95,0.99)(Duration)[3]/1000000) AS p95Ms, if(count()=0,NULL,quantilesTDigest(0.50,0.90,0.95,0.99)(Duration)[4]/1000000) AS p99Ms"
const severitySQL = "multiIf(SeverityNumber>=21,'fatal',SeverityNumber>=17,'error',SeverityNumber>=13,'warn',SeverityNumber>=9,'info',SeverityNumber>=5,'debug',SeverityNumber>=1,'trace','unspecified')"

func analyticsHandler(store queryStore, kind string, slots chan struct{}) gin.HandlerFunc {
	return func(c *gin.Context) {
		f, err := parseFilter(c)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid_query", "message": err.Error()})
			return
		}
		if kind == "logs-keys" || kind == "traces-keys" {
			f.DiscoveryScope = c.DefaultQuery("scope", map[bool]string{true: "log", false: "span"}[kind == "logs-keys"])
			f.KeySearch = c.Query("keySearch")
			if (f.DiscoveryScope != "resource" && f.DiscoveryScope != map[bool]string{true: "log", false: "span"}[kind == "logs-keys"]) || !utf8.ValidString(f.KeySearch) || utf8.RuneCountInString(f.KeySearch) > 128 || strings.ContainsFunc(f.KeySearch, unicode.IsControl) {
				c.JSON(400, gin.H{"error": "invalid_query", "message": "Invalid attribute scope or key search"})
				return
			}
			f.Attributes = nil
			f.Search = ""
			f.Limit = 50
			f.Offset = 0
		}
		if kind == "services" {
			f.Attributes = nil
		}
		for _, attribute := range f.Attributes {
			logs := strings.HasPrefix(kind, "logs")
			if (logs && attribute.Scope == "span") || (!logs && (attribute.Scope == "log" || attribute.Scope == "body")) {
				c.JSON(400, gin.H{"error": "invalid_query", "message": "attribute scope does not match this signal"})
				return
			}
		}
		actor, ok := c.Request.Context().Value(principalKey).(principal)
		if !ok || actor.Tenant == "" {
			c.JSON(403, gin.H{"error": "forbidden"})
			return
		}
		queries, list := compileQueries(f, kind, actor.Tenant)
		if kind == "logs-keys" || kind == "traces-keys" {
			list = true
		}
		if c.Query("preview") == "1" {
			c.Header("Cache-Control", "no-store")
			c.JSON(200, gin.H{"queries": previewQueries(queries), "from": f.From, "to": f.To})
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-c.Request.Context().Done():
			return
		case <-time.After(250 * time.Millisecond):
			c.JSON(429, gin.H{"error": "too_many_queries"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		data, err := store.Query(ctx, queries[0].SQL, queries[0].Args...)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "query_unavailable", "message": "Query unavailable or resource limit reached. Narrow the time range or service filter and retry."})
			return
		}
		truncated := list && len(data) > f.Limit
		if truncated {
			data = data[:f.Limit]
		}
		body := gin.H{"data": data, "from": f.From, "to": f.To, "truncated": truncated}
		if truncated && f.Offset+f.Limit <= 5000 && kind != "logs-keys" && kind != "traces-keys" {
			body["nextOffset"] = f.Offset + f.Limit
		}
		if kind == "red" {
			summary, err := store.Query(ctx, queries[1].SQL, queries[1].Args...)
			if err != nil {
				c.JSON(503, gin.H{"error": "query_unavailable"})
				return
			}
			if len(summary) > 0 {
				body["summary"] = summary[0]
			}
			body["data"] = fillBuckets(data, f, false)
		}
		if kind == "logs-volume" {
			body["data"] = fillBuckets(data, f, true)
		}
		c.JSON(200, body)
	}
}

func fillBuckets(rows []map[string]any, f queryFilter, logs bool) []map[string]any {
	index := map[string]map[string]any{}
	for _, row := range rows {
		if bucket, ok := row["bucket"].(time.Time); ok {
			index[bucket.UTC().Format(time.RFC3339)+fmt.Sprint(row["severity"])] = row
		}
	}
	result := make([]map[string]any, 0)
	for bucket := f.From.UTC().Truncate(time.Minute); bucket.Before(f.To); bucket = bucket.Add(time.Minute) {
		bands := []string{""}
		if logs {
			bands = []string{"unspecified", "trace", "debug", "info", "warn", "error", "fatal"}
		}
		for _, band := range bands {
			key := bucket.Format(time.RFC3339) + "<nil>"
			if logs {
				key = bucket.Format(time.RFC3339) + band
			}
			row := index[key]
			if row == nil {
				if logs {
					row = map[string]any{"bucket": bucket, "severity": band, "records": 0}
				} else {
					row = map[string]any{"bucket": bucket, "requests": 0, "errors": 0, "errorRate": 0, "p50Ms": nil, "p90Ms": nil, "p95Ms": nil, "p99Ms": nil}
				}
			}
			row["partial"] = bucket.Before(f.From) || bucket.Add(time.Minute).After(f.To)
			result = append(result, row)
		}
	}
	return result
}
