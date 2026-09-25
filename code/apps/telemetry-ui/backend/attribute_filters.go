package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type attributeFilter struct {
	Scope string `json:"scope"`
	Key   string `json:"key"`
	Op    string `json:"op"`
	Value string `json:"value,omitempty"`
	Path  string `json:"path,omitempty"`
}

func parseAttributes(values []string) ([]attributeFilter, error) {
	if len(values) > 8 {
		return nil, errors.New("at most 8 attribute conditions are allowed")
	}
	result := make([]attributeFilter, 0, len(values))
	for _, value := range values {
		if len(value) > 4096 {
			return nil, errors.New("attribute condition too long")
		}
		var item attributeFilter
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&item); err != nil {
			return nil, errors.New("invalid attribute condition JSON")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, errors.New("invalid attribute condition JSON")
		}
		if item.Scope != "resource" && item.Scope != "span" && item.Scope != "log" && item.Scope != "body" {
			return nil, errors.New("invalid attribute scope")
		}
		if !utf8.ValidString(item.Key) || len(item.Key) == 0 || utf8.RuneCountInString(item.Key) > 128 || strings.ContainsFunc(item.Key, unicode.IsControl) {
			return nil, errors.New("attribute key must contain 1–128 characters without controls")
		}
		if !utf8.ValidString(item.Value) || utf8.RuneCountInString(item.Value) > 256 || strings.ContainsFunc(item.Value, unicode.IsControl) {
			return nil, errors.New("attribute value must contain at most 256 characters without controls")
		}
		path := item.Path
		if item.Scope == "body" {
			if path != "" {
				return nil, errors.New("body uses key as its JSON path")
			}
			path = item.Key
		}
		if path != "" {
			if _, err := jsonPath(path); err != nil {
				return nil, err
			}
		}
		switch item.Op {
		case "eq", "neq", "contains":
			if item.Op == "contains" && item.Value == "" {
				return nil, errors.New("contains requires a value")
			}
		case "exists", "missing":
			if item.Value != "" {
				return nil, errors.New("exists/missing cannot have a value")
			}
		case "gt", "gte", "lt", "lte":
			n, err := strconv.ParseFloat(item.Value, 64)
			if err != nil || n != n || n > 1.7976931348623157e308 || n < -1.7976931348623157e308 {
				return nil, errors.New("numeric comparison requires a finite number")
			}
		default:
			return nil, errors.New("invalid attribute operator")
		}
		result = append(result, item)
	}
	return result, nil
}

func (f attributeFilter) sql() (string, []any) {
	if f.Scope == "body" || f.Path != "" {
		return f.jsonSQL()
	}
	// Only allowlisted map names and operators become SQL; keys/values are always bound.
	column := map[string]string{"resource": "ResourceAttributes", "span": "SpanAttributes", "log": "LogAttributes"}[f.Scope]
	exists := "mapContains(" + column + ", ?)"
	if f.Op == "exists" {
		return exists, []any{f.Key}
	}
	if f.Op == "missing" {
		return "NOT " + exists, []any{f.Key}
	}
	value := column + "[?]"
	args := []any{f.Key, f.Key}
	switch f.Op {
	case "eq", "neq":
		op := "="
		if f.Op == "neq" {
			op = "!="
		}
		return "(" + exists + " AND " + value + " " + op + " ?)", append(args, f.Value)
	case "contains":
		return "(" + exists + " AND positionCaseInsensitiveUTF8(" + value + ", ?) > 0)", append(args, f.Value)
	default:
		op := map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[f.Op]
		n, _ := strconv.ParseFloat(f.Value, 64)
		return fmt.Sprintf("(%s AND toFloat64OrNull(%s) %s ?)", exists, value, op), append(args, n)
	}
}

// Dot paths use zero-based numeric array indexes; every segment is bound.
func jsonPath(path string) ([]any, error) {
	parts := strings.Split(path, ".")
	if len(parts) > 8 || utf8.RuneCountInString(path) > 128 {
		return nil, errors.New("JSON path supports up to 8 segments and 128 characters")
	}
	args := make([]any, 0, len(parts))
	for _, part := range parts {
		if part == "" || strings.ContainsFunc(part, unicode.IsControl) {
			return nil, errors.New("JSON path segments must not be empty or contain controls")
		}
		numeric := strings.Trim(part, "0123456789") == ""
		if numeric {
			index, err := strconv.Atoi(part)
			if err != nil || index > 1000000 {
				return nil, errors.New("JSON array index must be between 0 and 1000000")
			}
			args = append(args, index+1)
		} else {
			args = append(args, part)
		}
	}
	return args, nil
}

func (f attributeFilter) jsonSQL() (string, []any) {
	source, path := "Body", f.Key
	var sourceArgs []any
	if f.Scope != "body" {
		source = map[string]string{"resource": "ResourceAttributes", "span": "SpanAttributes", "log": "LogAttributes"}[f.Scope] + "[?]"
		sourceArgs = []any{f.Key}
		path = f.Path
	}
	segments, _ := jsonPath(path)
	extract := "JSONExtractRaw(" + source + strings.Repeat(", ?", len(segments)) + ")"
	extractArgs := append(append([]any{}, sourceArgs...), segments...)
	// Invalid JSON is excluded even for Missing, rather than treated as an empty object.
	valid := "isValidJSON(" + source + ")"
	exists := extract + " != ''"
	if f.Op == "exists" || f.Op == "missing" {
		if f.Op == "missing" {
			exists = extract + " = ''"
		}
		return "(" + valid + " AND " + exists + ")", append(append([]any{}, sourceArgs...), extractArgs...)
	}
	// Decode string scalars; keep numeric/boolean/null representations for comparisons.
	value := "if(startsWith(" + extract + ", '\"'), JSONExtractString(" + extract + "), " + extract + ")"
	args := append(append([]any{}, sourceArgs...), extractArgs...)
	for i := 0; i < 3; i++ {
		args = append(args, extractArgs...)
	}
	predicate := ""
	switch f.Op {
	case "eq", "neq":
		op := "="
		if f.Op == "neq" {
			op = "!="
		}
		predicate = value + " " + op + " ?"
		args = append(args, f.Value)
	case "contains":
		predicate = "positionCaseInsensitiveUTF8(" + value + ", ?) > 0"
		args = append(args, f.Value)
	default:
		op := map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<="}[f.Op]
		n, _ := strconv.ParseFloat(f.Value, 64)
		predicate = "toFloat64OrNull(" + value + ") " + op + " ?"
		args = append(args, n)
	}
	return "(" + valid + " AND " + exists + " AND " + predicate + ")", args
}
