package mock

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func matchAttribute(record Record, attr query.AttributeFilter) bool {
	attributes := record.Attributes
	if attr.Scope == "resource" {
		attributes = record.ResourceAttributes
	}
	value, exists := attributes[attr.Key]
	if attr.Scope == "body" || attr.Path != "" {
		path := attr.Path
		if attr.Scope == "body" {
			value = record.Body
			path = attr.Key
		}
		var valid bool
		value, exists, valid = jsonValue(value, path)
		// Invalid JSON does not match even a "missing" condition.
		if !valid {
			return false
		}
	}
	switch attr.Op {
	case "exists":
		return exists
	case "missing":
		return !exists
	}
	if !exists {
		return false
	}
	switch attr.Op {
	case "eq":
		return value == attr.Value
	case "neq":
		return value != attr.Value
	case "contains":
		return contains(value, attr.Value)
	}
	left, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return false
	}
	right, err := strconv.ParseFloat(attr.Value, 64)
	if err != nil {
		return false
	}
	switch attr.Op {
	case "gt":
		return left > right
	case "gte":
		return left >= right
	case "lt":
		return left < right
	case "lte":
		return left <= right
	default:
		return false
	}
}

// jsonValue distinguishes an absent path from invalid JSON.
func jsonValue(source, path string) (value string, exists, valid bool) {
	var node any
	if json.Unmarshal([]byte(source), &node) != nil {
		return "", false, false
	}
	for _, segment := range strings.Split(path, ".") {
		var found bool
		node, found = jsonChild(node, segment)
		if !found {
			return "", false, true
		}
	}
	if text, ok := node.(string); ok {
		return text, true, true
	}
	encoded, _ := json.Marshal(node)
	return string(encoded), true, true
}

func jsonChild(node any, segment string) (any, bool) {
	switch current := node.(type) {
	case map[string]any:
		value, exists := current[segment]
		return value, exists
	case []any:
		index, err := strconv.Atoi(segment)
		if err != nil || index < 0 || index >= len(current) {
			return nil, false
		}
		return current[index], true
	default:
		return nil, false
	}
}
