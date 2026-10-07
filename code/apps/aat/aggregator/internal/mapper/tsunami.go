package mapper

import (
	"aat/aggregator/internal/model"
	"fmt"
	"time"
)

func Tsunami(r model.Record) (id, ref string, err error) {
	id, err = String(r, "warning_id")
	if err != nil {
		return
	}

	ref, err = String(r, "related_event_id")
	if err != nil {
		return
	}

	level, e := String(r, "threat_level")
	if e != nil || Rank[Levels[level]] < 1 {
		err = fmt.Errorf("invalid threat_level")
		return
	}

	if _, err = Read[[]string](r, "affected_zones"); err != nil {
		return
	}

	_, err = Read[time.Time](r, "estimated_arrival")
	return
}
