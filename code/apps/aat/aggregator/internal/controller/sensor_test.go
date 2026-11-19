package controller

import (
	"encoding/json"
	"testing"
)

func TestCriticalFirstMovesCriticalReadingsToTheFront(t *testing.T) {
	in := []json.RawMessage{
		json.RawMessage(`{"observation_id":"a","critical":false}`),
		json.RawMessage(`{"observation_id":"b","critical":true}`),
		json.RawMessage(`{"observation_id":"c"`), // broken: stays in the ordinary group
		json.RawMessage(`{"observation_id":"d","critical":false}`),
		json.RawMessage(`{"observation_id":"e","critical":true}`),
	}
	out := criticalFirst(in)
	got := ""
	for _, raw := range out {
		got += string(raw)[19:20]
	}
	if got != "beacd" {
		t.Fatalf("order %q, want critical readings b, e first, others in their original order", got)
	}
}
