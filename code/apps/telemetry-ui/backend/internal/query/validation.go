package query

import (
	"errors"
	"strings"
)

// ValidateScope is also used by model callers that do not pass through HTTP parsing.
// SQL identifiers and operators must never be selected from unvalidated input.
func (f Filter) ValidateScope(tenant, signal string, discovery bool) error {
	if strings.TrimSpace(tenant) == "" {
		return errors.New("missing tenant scope")
	}
	if discovery && f.DiscoveryScope != "resource" && f.DiscoveryScope != signal {
		return errors.New("invalid attribute discovery scope")
	}

	if len(f.Attributes) > 8 {
		return errors.New("at most 8 attribute conditions are allowed")
	}
	for _, attr := range f.Attributes {
		if (signal == "log" && attr.Scope == "span") || (signal == "span" && (attr.Scope == "log" || attr.Scope == "body")) {
			return errors.New("attribute scope does not match signal")
		}
		if err := attr.validate(); err != nil {
			return err
		}
	}
	return nil
}
