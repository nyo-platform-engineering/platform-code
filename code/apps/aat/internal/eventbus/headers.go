package eventbus

import (
	"github.com/nats-io/nats.go"
	"strings"
)

// HeaderCarrier preserves NATS header keys. Casting NATS headers to http.Header
// is unsafe: HTTP Get canonicalizes keys, but NATS preserves their original case.
type HeaderCarrier nats.Header

func (h HeaderCarrier) Get(key string) string {
	if value := nats.Header(h).Get(key); value != "" {
		return value
	}
	for existing, values := range h {
		if strings.EqualFold(existing, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}
func (h HeaderCarrier) Set(key, value string) { nats.Header(h).Set(key, value) }
func (h HeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(h))
	for key := range h {
		keys = append(keys, key)
	}
	return keys
}
