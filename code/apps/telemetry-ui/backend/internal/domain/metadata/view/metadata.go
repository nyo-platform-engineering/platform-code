package view

import "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/auth"

func Metadata(version string, actor auth.Principal) map[string]any {
	return map[string]any{
		"service": "telemetry-ui",
		"version": version,
		"actor":   actor,
		"capabilities": []Capability{
			{ID: "trace-red", Title: "Trace RED", Status: "available", Bucket: "1m", Description: "Request rate, errors, and duration grouped by service."},
			{ID: "log-volume", Title: "Log volume by severity", Status: "available", Bucket: "1m", Description: "Log counts grouped into OpenTelemetry severity bands."},
		},
	}
}

type Capability struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Bucket      string `json:"bucket"`
	Description string `json:"description"`
}
