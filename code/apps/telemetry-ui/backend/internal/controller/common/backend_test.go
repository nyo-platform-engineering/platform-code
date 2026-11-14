package common

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	model "github.com/nyo-platform-engineering/platform-code/code/apps/telemetry-ui/backend/internal/query"
)

type requestMock struct {
	request model.Request
	result  model.Result
}

func (s *requestMock) Query(context.Context, string, ...any) ([]map[string]any, error) {
	return nil, errors.New("mock received SQL")
}
func (s *requestMock) Execute(_ context.Context, request model.Request) (model.Result, error) {
	s.request = request
	return s.result, nil
}

func TestBackendSelectionPreservesRequest(t *testing.T) {
	end := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		signal    model.Signal
		operation model.Operation
	}{
		{model.SignalTraces, model.OperationRecords}, {model.SignalTraces, model.OperationDetail},
		{model.SignalTraces, model.OperationMetrics}, {model.SignalTraces, model.OperationAttributes},
		{model.SignalLogs, model.OperationRecords}, {model.SignalLogs, model.OperationMetrics},
		{model.SignalLogs, model.OperationAttributes}, {model.SignalTraces, model.OperationServices},
		{model.SignalLogs, model.OperationServices},
	} {
		t.Run(string(tc.signal)+"/"+string(tc.operation), func(t *testing.T) {
			request := model.Request{Signal: tc.signal, Operation: tc.operation, OrganizationScope: "trusted-tenant", Filter: model.Filter{From: end.Add(-time.Hour), To: end, Limit: 2, Offset: 4, Services: []string{"checkout"}, Search: "needle"}}
			want := model.Result{Data: []map[string]any{{"service": "checkout"}}, Summary: []map[string]any{{"requests": 42}}}
			mock := &requestMock{result: want}
			result, err := Handler(true, mock.Execute, QueryHandler(mock, func(model.Request) ([]model.CompiledQuery, error) {
				t.Fatal("mock execution compiled SQL")
				return nil, nil
			}))(context.Background(), request)
			if err != nil || !reflect.DeepEqual(mock.request, request) || !reflect.DeepEqual(result, want) {
				t.Fatalf("mock changed request or result: %+v %v", result, err)
			}
			database := &captureStore{}
			_, err = Handler(false, mock.Execute, QueryHandler(database, func(got model.Request) ([]model.CompiledQuery, error) {
				if !reflect.DeepEqual(got, request) {
					t.Fatalf("compiler received different request: %+v", got)
				}
				return []model.CompiledQuery{{SQL: "SELECT ?", Args: []any{request.OrganizationScope}}}, nil
			}))(context.Background(), request)
			if err != nil || len(database.queries) != 1 || database.queries[0] != "SELECT ?" || !reflect.DeepEqual(database.args[0], []any{request.OrganizationScope}) {
				t.Fatalf("bound SQL was not executed: %+v %v", database, err)
			}
		})
	}
}
