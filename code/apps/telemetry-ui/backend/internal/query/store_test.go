package query

import (
	"os"
	"testing"
)

func TestSignalConnectionSettings(t *testing.T) {
	for _, signal := range []string{"TRACES_", "LOGS_"} {
		for _, key := range []string{"ADDR", "USER", "PASSWORD"} {
			name := "CLICKHOUSE_" + signal + key
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("CLICKHOUSE_ADDR", "shared:9000")
	t.Setenv("CLICKHOUSE_USER", "shared-user")
	t.Setenv("CLICKHOUSE_PASSWORD", "shared-pass")
	for _, signal := range []string{"TRACES_", "LOGS_"} {
		options := connectionOptions(signal)
		if options.Addr[0] != "shared:9000" || options.Auth.Username != "shared-user" || options.Auth.Password != "shared-pass" {
			t.Fatal("signal did not inherit shared settings")
		}
	}
	t.Setenv("CLICKHOUSE_TRACES_ADDR", "traces:9000")
	t.Setenv("CLICKHOUSE_TRACES_USER", "trace-reader")
	t.Setenv("CLICKHOUSE_TRACES_PASSWORD", "trace-pass")
	t.Setenv("CLICKHOUSE_LOGS_ADDR", "logs:9000")
	t.Setenv("CLICKHOUSE_LOGS_USER", "log-reader")
	t.Setenv("CLICKHOUSE_LOGS_PASSWORD", "")
	traces, logs := connectionOptions("TRACES_"), connectionOptions("LOGS_")
	if traces.Addr[0] != "traces:9000" || traces.Auth.Username != "trace-reader" || traces.Auth.Password != "trace-pass" {
		t.Fatal("trace overrides ignored")
	}
	if logs.Addr[0] != "logs:9000" || logs.Auth.Username != "log-reader" || logs.Auth.Password != "" {
		t.Fatal("log overrides or empty password ignored")
	}
}

func TestOpenStoresSharesOnlyIdenticalConnections(t *testing.T) {
	for _, signal := range []string{"TRACES_", "LOGS_"} {
		t.Setenv("CLICKHOUSE_"+signal+"ADDR", "same:9000")
		t.Setenv("CLICKHOUSE_"+signal+"USER", "reader")
		t.Setenv("CLICKHOUSE_"+signal+"PASSWORD", "secret")
	}
	traces, logs := OpenStores()
	if traces != logs {
		t.Fatal("identical configuration must share a pool")
	}
	traces.Close()
	t.Setenv("CLICKHOUSE_LOGS_PASSWORD", "different")
	traces, logs = OpenStores()
	defer traces.Close()
	defer logs.Close()
	if traces == logs {
		t.Fatal("different credentials must not share a pool")
	}
}
