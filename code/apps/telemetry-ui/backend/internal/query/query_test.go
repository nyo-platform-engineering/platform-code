package query_test

import (
	"testing"

	logs "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/logs/model"
	metadata "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/metadata/model"
	traces "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/domain/traces/model"
	"github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

func TestCompilersRejectUnscopedOrInvalidQueries(t *testing.T) {
	for _, tc := range []struct {
		signal         query.Signal
		attributeScope string
		compile        query.Compiler
	}{
		{query.SignalTraces, "span", traces.CompileQueries},
		{query.SignalLogs, "log", logs.CompileQueries},
		{query.SignalTraces, "", metadata.CompileQueries},
	} {
		f := query.Filter{Limit: 100}
		operation := query.OperationRecords
		if tc.attributeScope == "" {
			operation = query.OperationServices
		}
		if _, err := tc.compile(query.Request{Filter: f, Signal: tc.signal, Operation: operation, OrganizationScope: ""}); err == nil {
			t.Fatalf("%s accepted empty organization scope", tc.signal)
		}
		if _, err := tc.compile(query.Request{Filter: f, Signal: tc.signal, Operation: query.Operation("injected"), OrganizationScope: "tenant"}); err == nil {
			t.Fatalf("%s accepted invalid operation", tc.signal)
		}
		if tc.attributeScope == "" {
			continue
		}
		for _, attr := range []query.AttributeFilter{
			{Scope: "resource); DROP TABLE x;--", Key: "x", Op: "exists"},
			{Scope: tc.attributeScope, Key: "x", Op: "= 1 OR 1=1"},
			{Scope: tc.attributeScope, Key: "payload", Path: "a..b", Op: "exists"},
		} {
			f.Attributes = []query.AttributeFilter{attr}
			if _, err := tc.compile(query.Request{Filter: f, Signal: tc.signal, Operation: query.OperationRecords, OrganizationScope: "tenant"}); err == nil {
				t.Fatalf("accepted unsafe attribute: %#v", attr)
			}
		}
		f.Attributes = nil
		f.DiscoveryScope = "Body) FROM secret --"
		if _, err := tc.compile(query.Request{Filter: f, Signal: tc.signal, Operation: query.OperationAttributes, OrganizationScope: "tenant"}); err == nil {
			t.Fatal("accepted arbitrary discovery identifier")
		}
	}
}
