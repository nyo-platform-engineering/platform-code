package main

import (
	"encoding/json"
	"testing"

	"aat/consumers/internal/worker"
)

func TestRegularLaneDelegatesCriticalSensorAlerts(t *testing.T) {
	for _, tc := range []struct {
		name, source, severity, attrs, want string
	}{
		{"critical sensor", "SENSOR_PANTAI", "AWAS", `{"critical":true,"verified":true}`, "critical_alert_delegated"},
		{"unverified critical sensor", "SENSOR_PANTAI", "AWAS", `{"critical":true,"verified":false}`, "critical_alert_delegated"},
		{"regular sensor", "SENSOR_PANTAI", "SIAGA", `{"critical":false}`, "field_alert"},
		{"other source", "PVMBG", "AWAS", `{"critical":true}`, "field_alert"},
		{"non alert", "BMKG", "NORMAL", `{}`, "no_field_alert"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hazard := worker.Hazard{Source: tc.source, Severity: tc.severity, Attributes: json.RawMessage(tc.attrs)}
			if got := regularKind(hazard); got != tc.want {
				t.Fatalf("regular lane kind=%s, want %s", got, tc.want)
			}
		})
	}
}
