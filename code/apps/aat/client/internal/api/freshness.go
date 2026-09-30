package api

import (
	"context"
	"time"
)

type sourceRecord struct {
	Source      string     `json:"source"`
	Healthy     *bool      `json:"healthy"`
	LastSuccess *time.Time `json:"last_success_at"`
}

type SourceStatus struct {
	Source      string     `json:"source"`
	Status      string     `json:"status"`
	LastSuccess *time.Time `json:"last_success_at"`
	Stale       bool       `json:"stale"`
}

func (u upstream) statuses(ctx context.Context, filter, correlation string) []SourceStatus {
	var body struct {
		Sources []sourceRecord `json:"sources"`
	}
	err := u.get(ctx, "GET", u.endpoint(u.config.AggregatorURL, "/internal/source-status"), u.config.AggregatorToken, correlation, nil, &body)
	entries := map[string][]sourceRecord{}
	if err == nil {
		for _, r := range body.Sources {
			entries[r.Source] = append(entries[r.Source], r)
		}
	}
	now := time.Now()
	result := []SourceStatus{}
	for _, source := range []string{"BMKG", "PVMBG"} {
		if filter != "" && filter != source {
			continue
		}
		status := SourceStatus{Source: source, Status: "unknown", Stale: true}
		if records := entries[source]; len(records) == 1 {
			r := records[0]
			if r.Healthy != nil && r.LastSuccess != nil && !r.LastSuccess.IsZero() && !r.LastSuccess.After(now) {
				status.LastSuccess = r.LastSuccess
				status.Stale = !*r.Healthy || now.Sub(*r.LastSuccess) > u.config.StaleAfter
				status.Status = "fresh"
				if status.Stale {
					status.Status = "stale"
				}
			}
		}
		result = append(result, status)
	}
	return result
}
