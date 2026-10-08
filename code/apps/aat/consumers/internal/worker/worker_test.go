package worker

import (
	"testing"
	"time"
)

func TestDeliveryKeyPreservesHazardUpdates(t *testing.T) {
	hazard := Hazard{ID: "haz-one", Ingested: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}
	first := deliveryKey("first-version", hazard)
	if first != deliveryKey("first-version", hazard) {
		t.Fatal("redelivery changed key")
	}
	if first == deliveryKey("new-warning-version", hazard) {
		t.Fatal("a severity update would be dropped")
	}
	fallback := deliveryKey("", hazard)
	hazard.Ingested = hazard.Ingested.Add(time.Second)
	if fallback == deliveryKey("", hazard) {
		t.Fatal("legacy updates would be dropped")
	}
	for _, key := range []string{first, fallback} {
		if len(key) != 64 {
			t.Fatal("invalid key length")
		}
		for _, c := range key {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				t.Fatal("unsafe KV key")
			}
		}
	}
}
