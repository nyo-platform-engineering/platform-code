package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"aat/aggregator/internal/controller"
	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
	"aat/internal/httpkit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type freezeProbeResult struct {
	Phase             string          `json:"phase"`
	Fence             model.PollOwner `json:"fence"`
	ValidateRejected  bool            `json:"validate_rejected"`
	CaptureRejected   bool            `json:"capture_rejected"`
	CanonicalRejected bool            `json:"canonical_rejected"`
	StatusRejected    bool            `json:"status_rejected"`
	ReacquireBusy     bool            `json:"reacquire_busy"`
}

func probeBatch(ref string) map[string][]model.Record {
	var record model.Record
	_ = json.Unmarshal([]byte(strings.Replace(m2VolcanicReport, "m2-v1", ref, 1)), &record)
	return map[string][]model.Record{"volcanic_reports": {record}}
}

func TestM2FreezeProbeChild(t *testing.T) {
	schema := os.Getenv("AAT_FENCE_CHILD_SCHEMA")
	if schema == "" {
		t.Skip("subprocess helper")
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	config, err := pgx.ParseConfig(os.Getenv("AAT_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema
	pool := stdlib.OpenDB(*config)
	defer pool.Close()
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	ctrl := &controller.Controller{DB: db}
	ctx := context.Background()
	old, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	send := func(result freezeProbeResult) {
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			t.Fatal(err)
		}
	}
	input := bufio.NewScanner(os.Stdin)
	receive := func(expected string) {
		if !input.Scan() || input.Text() != expected {
			t.Fatalf("expected probe command %s", expected)
		}
	}
	send(freezeProbeResult{Phase: "ready", Fence: old.Fence})
	receive("stale")
	validateErr := old.Controller.ValidatePollOwner(ctx, "PVMBG")
	raw := []byte("[" + strings.Replace(m2VolcanicReport, "m2-v1", "stale-raw", 1) + "]")
	captureErr := old.Controller.CaptureRawPoll(ctx, "PVMBG", []controller.RawPayload{{BatchKey: "volcanic_reports", Raw: raw}})
	_, canonicalErr := old.Controller.IngestPollBatch(ctx, "PVMBG", probeBatch("stale-canonical"), nil)
	statusErr := old.Controller.MarkPollFailure(ctx, "PVMBG", time.Now().Add(-30*time.Second), "stale owner")
	attempt, busyErr := ctrl.AcquirePollSession(ctx, "PVMBG")
	if attempt != nil {
		attempt.Close()
	}
	send(freezeProbeResult{Phase: "stale_attempts", Fence: old.Fence, ValidateRejected: validateErr != nil, CaptureRejected: captureErr != nil,
		CanonicalRejected: canonicalErr != nil, StatusRejected: statusErr != nil, ReacquireBusy: errors.Is(busyErr, controller.ErrPollBusy)})
	receive("renew")
	renewed, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer renewed.Close()
	changed, err := renewed.Controller.IngestPollBatch(ctx, "PVMBG", probeBatch("renewed-owner"), nil)
	if err != nil || len(changed) != 1 {
		t.Fatalf("renewed owner could not commit: changed=%d err=%v", len(changed), err)
	}
	send(freezeProbeResult{Phase: "renewed", Fence: renewed.Fence})
}

func TestM2FrozenProcessCannotWriteUntilOwnershipRenewed(t *testing.T) {
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	ctrl := pollController(t)
	ctx := context.Background()
	distributed := os.Getenv("AAT_TEST_CITUS") == "1"
	if distributed {
		if err := storage.EnableCitus(ctx, ctrl.DB); err != nil {
			t.Fatal(err)
		}
	}
	var schema string
	if err := ctrl.DB.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestM2FreezeProbeChild$")
	cmd.Env = append(os.Environ(), "AAT_FENCE_CHILD_SCHEMA="+schema)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Signal(syscall.SIGCONT)
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	results := make(chan freezeProbeResult, 4)
	decoding := make(chan error, 1)
	go func() {
		decoder := json.NewDecoder(stdout)
		for i := 0; i < 3; i++ {
			var result freezeProbeResult
			if err := decoder.Decode(&result); err != nil {
				decoding <- err
				return
			}
			results <- result
		}
	}()
	receive := func(phase string) freezeProbeResult {
		t.Helper()
		select {
		case result := <-results:
			if result.Phase != phase {
				t.Fatalf("expected %s, got %s", phase, result.Phase)
			}
			return result
		case err := <-decoding:
			t.Fatalf("probe output: %v", err)
		case <-time.After(15 * time.Second):
			t.Fatalf("probe timed out waiting for %s", phase)
		}
		return freezeProbeResult{}
	}
	ready := receive("ready")
	if other, err := ctrl.AcquirePollSession(ctx, "PVMBG"); !errors.Is(err, controller.ErrPollBusy) {
		if other != nil {
			other.Close()
		}
		t.Fatalf("initial ownership was not exclusive: %v", err)
	}
	if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	frozen := time.Now()
	t.Log("FENCING_PHASE child SIGSTOP; waiting 23s, beyond PostgreSQL's production 20s idle timeout")
	time.Sleep(23 * time.Second)
	next, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatalf("takeover while child frozen: %v", err)
	}
	defer next.Close()
	if next.Fence.Epoch <= ready.Fence.Epoch {
		t.Fatal("takeover did not advance epoch")
	}
	if changed, err := next.Controller.IngestPollBatch(ctx, "PVMBG", probeBatch("successor"), nil); err != nil || len(changed) != 1 {
		t.Fatalf("successor commit: changed=%d err=%v", len(changed), err)
	}
	frozenSeconds := time.Since(frozen).Seconds()
	if err := cmd.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(stdin, "stale"); err != nil {
		t.Fatal(err)
	}
	stale := receive("stale_attempts")
	if !stale.ValidateRejected || !stale.CaptureRejected || !stale.CanonicalRejected || !stale.StatusRejected || !stale.ReacquireBusy {
		t.Fatalf("old process bypassed fencing: %+v", stale)
	}
	for table, want := range map[string]int64{"hazard_events": 1, "hazard_outboxes": 1, "inbox_messages": 0} {
		var count int64
		if err := ctrl.DB.Table(table).Count(&count).Error; err != nil || count != want {
			t.Fatalf("stale write changed %s: got=%d want=%d err=%v", table, count, want, err)
		}
	}
	state, err := ctrl.GetPollState(ctx, "PVMBG")
	if err != nil || !state.Healthy || state.LastError != nil {
		t.Fatalf("stale process changed source state: %+v %v", state, err)
	}
	next.Close()
	if _, err := fmt.Fprintln(stdin, "renew"); err != nil {
		t.Fatal(err)
	}
	renewed := receive("renewed")
	if renewed.Fence.Epoch <= next.Fence.Epoch || renewed.Fence.BackendPID == ready.Fence.BackendPID {
		t.Fatal("resumed process reused stale authority")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	waited = true
	var count int64
	if err := ctrl.DB.Model(&model.HazardEvent{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("renewed process did not commit exactly one event: count=%d err=%v", count, err)
	}
	evidence := map[string]any{"scenario": "process SIGSTOP/SIGCONT; not VM suspension", "frozen_seconds": frozenSeconds,
		"distributed_canonical":           distributed,
		"production_idle_timeout_seconds": 20, "before": ready.Fence, "takeover": next.Fence, "stale_attempts": stale, "renewed": renewed.Fence,
		"hazards_after_stale_attempt": 1, "hazards_after_renewal": 2, "stale_raw_captures": 0}
	encoded, _ := json.Marshal(evidence)
	t.Log("FENCING_EVIDENCE " + string(encoded))
}

func TestM2OldFenceCannotMoveToFreshDatabaseConnection(t *testing.T) {
	ctrl := pollController(t)
	ctx := context.Background()
	old, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	rebound := *old.Controller
	rebound.DB = ctrl.DB
	if _, err := rebound.IngestPollBatch(ctx, "PVMBG", probeBatch("wrong-backend"), nil); !errors.Is(err, controller.ErrPollFenced) {
		t.Fatalf("old identity reused another backend: %v", err)
	}
	if _, err := old.Controller.IngestPollBatch(ctx, "PVMBG", probeBatch("valid-backend"), nil); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := ctrl.DB.Model(&model.HazardEvent{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("backend fencing leaked writes: count=%d err=%v", count, err)
	}
}

func TestM2StorageReceiptRequiresSuccessfulCommit(t *testing.T) {
	ctrl := pollController(t)
	ctx := context.Background()
	owner, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := ctrl.DB.Exec(`CREATE FUNCTION reject_poll_commit() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'forced commit failure'; RETURN NEW; END $$;
 CREATE CONSTRAINT TRIGGER reject_poll_commit AFTER INSERT ON hazard_outboxes
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_poll_commit();`).Error; err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	if _, err := owner.Controller.IngestPollBatch(ctx, "PVMBG", probeBatch("commit-check"), nil); err == nil {
		t.Fatal("expected deferred COMMIT failure")
	}
	for _, table := range []string{"hazard_events", "hazard_outboxes"} {
		var count int64
		if err := ctrl.DB.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("rollback leaked %s: %d %v", table, count, err)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] == "poll storage committed" {
			t.Fatal("commit receipt emitted for rolled-back transaction")
		}
	}
	if err := ctrl.DB.Exec("DROP TRIGGER reject_poll_commit ON hazard_outboxes").Error; err != nil {
		t.Fatal(err)
	}
	buffer.Reset()
	if _, err := owner.Controller.IngestPollBatch(ctx, "PVMBG", probeBatch("commit-check"), nil); err != nil {
		t.Fatal(err)
	}
	commits := 0
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] != "poll storage committed" {
			continue
		}
		commits++
		if record["backend_pid"] != float64(owner.Fence.BackendPID) || record["epoch"] != float64(owner.Fence.Epoch) || record["owner"] != owner.Fence.Owner || record["transaction_id"] == "" || record["db_observed_at"] == "" || record["correlation_id"] == "" {
			t.Fatalf("incomplete commit receipt: %+v", record)
		}
	}
	if commits != 1 {
		t.Fatalf("expected one acknowledged commit, got %d", commits)
	}
}

func TestM2InboxReceiptsCoverCaptureAndProcessing(t *testing.T) {
	ctrl := pollController(t)
	ctx := httpkit.WithCorrelationID(context.Background(), "fencing-inbox")
	if os.Getenv("AAT_TEST_CITUS") == "1" {
		if err := storage.EnableCitus(ctx, ctrl.DB); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	var buffer bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })
	raw := []byte("[" + m2VolcanicReport + "]")
	if err := owner.Controller.CaptureRawPoll(ctx, "PVMBG", []controller.RawPayload{{BatchKey: "volcanic_reports", Raw: raw}}); err != nil {
		t.Fatal(err)
	}
	if changed, err := owner.Controller.ProcessInbox(ctx, "PVMBG", 100); err != nil || changed != 1 {
		t.Fatalf("inbox processing: changed=%d err=%v", changed, err)
	}
	operations := map[string]int{}
	transactions := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] != "poll storage committed" {
			continue
		}
		id, _ := record["transaction_id"].(string)
		if id == "" || transactions[id] {
			t.Fatal("missing or reused receipt transaction ID")
		}
		transactions[id] = true
		operation, _ := record["operation"].(string)
		operations[operation]++
		if record["backend_pid"] != float64(owner.Fence.BackendPID) || record["epoch"] != float64(owner.Fence.Epoch) || record["owner"] != owner.Fence.Owner || record["correlation_id"] != "fencing-inbox" {
			t.Fatalf("receipt does not match the owning session: %+v", record)
		}
		if _, err := time.Parse(time.RFC3339Nano, record["db_observed_at"].(string)); err != nil {
			t.Fatal(err)
		}
		t.Log("STORAGE_EVIDENCE " + line)
	}
	if operations["raw_capture"] != 1 || operations["inbox_envelope"] != 1 || operations["inbox_record"] != 1 || len(transactions) != 3 {
		t.Fatalf("wrong storage receipt coverage: %+v", operations)
	}
}
