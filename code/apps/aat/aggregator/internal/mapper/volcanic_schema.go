package mapper

import (
	"encoding/json"
	"fmt"
	"strconv"

	"aat/aggregator/internal/model"
)

// VolcanicV2 accepts the declared level alias, integer strings, and optional counts.
func VolcanicV2(raw model.Record) (model.HazardEvent, error) {
	record := make(model.Record, len(raw))
	for key, value := range raw {
		record[key] = value
	}
	if level, exists := record["level"]; exists {
		if _, ambiguous := record["alert_level"]; ambiguous {
			return model.HazardEvent{}, fmt.Errorf("ambiguous alert_level and level")
		}
		record["alert_level"] = level
		delete(record, "level")
	}
	if value, exists := record["eruption_count_24h"]; exists {
		var text string
		if json.Unmarshal(value, &text) == nil {
			count, err := strconv.Atoi(text)
			if err != nil || strconv.Itoa(count) != text {
				return model.HazardEvent{}, fmt.Errorf("invalid integer count")
			}
			record["eruption_count_24h"], _ = json.Marshal(count)
		}
	}
	return volcanic(record, true)
}
