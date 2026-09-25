package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
)

type demoParameters struct {
	Customer string `json:"customer_id"`
	Region   string `json:"region"`
	Product  string `json:"product"`
	Quantity int    `json:"quantity"`
	DelayMS  int    `json:"delay_ms"`
	Source   string `json:"source"`
}

func parseDemoParameters(r *http.Request, action string) (demoParameters, error) {
	p := demoParameters{Customer: "customer-01", Region: "ap-southeast-1", Product: "book", Quantity: 1, Source: "manual"}
	if action == "slow" {
		p.DelayMS = 500
	}
	q := r.URL.Query()
	for _, field := range []struct {
		key     string
		target  *string
		allowed []string
	}{
		{"customer_id", &p.Customer, []string{"customer-01", "customer-02", "customer-03", "customer-04", "customer-05"}},
		{"region", &p.Region, []string{"ap-southeast-1", "eu-west-1", "us-east-1"}},
		{"product", &p.Product, []string{"book", "keyboard", "coffee", "headphones"}},
		{"source", &p.Source, []string{"manual", "bot"}},
	} {
		values, exists := q[field.key]
		if !exists {
			continue
		}
		if len(values) != 1 {
			return p, fmt.Errorf("%s must occur once", field.key)
		}
		valid := false
		for _, allowed := range field.allowed {
			if values[0] == allowed {
				valid = true
			}
		}
		if !valid {
			return p, fmt.Errorf("invalid %s", field.key)
		}
		*field.target = values[0]
	}
	for _, field := range []struct {
		key      string
		target   *int
		min, max int
	}{{"quantity", &p.Quantity, 1, 10}, {"delay_ms", &p.DelayMS, 0, 2000}} {
		values, exists := q[field.key]
		if !exists {
			continue
		}
		if len(values) != 1 {
			return p, fmt.Errorf("%s must occur once", field.key)
		}
		n, err := strconv.Atoi(values[0])
		if err != nil || n < field.min || n > field.max {
			return p, fmt.Errorf("%s must be %d–%d", field.key, field.min, field.max)
		}
		*field.target = n
	}
	return p, nil
}
func (p demoParameters) attributes(action string) []attribute.KeyValue {
	payload, _ := json.Marshal(map[string]any{"customer": map[string]string{"id": p.Customer, "region": p.Region}, "cart": map[string]any{"product": p.Product, "quantity": p.Quantity}, "action": action})
	return []attribute.KeyValue{attribute.String("demo.source", p.Source), attribute.String("demo.customer_id", p.Customer), attribute.String("demo.region", p.Region), attribute.String("demo.product", p.Product), attribute.Int("demo.quantity", p.Quantity), attribute.Int("demo.delay_ms", p.DelayMS), attribute.String("demo.payload", string(payload))}
}
