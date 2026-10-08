package mapper

import (
	"aat/aggregator/internal/model"
	"fmt"
	"time"
)

func Tsunami(r model.Record) (id, ref string, err error) {
	// Read the references first so callers retain the same partial results on error.
	id, err = String(r, "warning_id")
	if err != nil {
		return
	}

	ref, err = String(r, "related_event_id")
	if err != nil {
		return
	}

	err = validateTsunami(r)
	return
}

func validateTsunami(r model.Record) error {
	return validateFields(r,
		requiredString("warning_id"),
		requiredString("related_event_id"),
		validateThreatLevel,
		requiredField[[]string]("affected_zones"),
		requiredField[time.Time]("estimated_arrival"),
	)
}

func validateThreatLevel(r model.Record) error {
	level, err := String(r, "threat_level")
	if err != nil || Rank[Levels[level]] < 1 {
		return fmt.Errorf("invalid threat_level")
	}
	return nil
}
