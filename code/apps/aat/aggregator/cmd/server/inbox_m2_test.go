package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	aggregator "aat/aggregator/internal"
	"aat/aggregator/internal/controller"
	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
	"aat/internal/httpkit"
	"net/http/httptest"
)

func captureReports(t *testing.T, ctrl *controller.Controller, records ...string) {
	t.Helper()
	if err := ctrl.CaptureRawPoll(context.Background(), "PVMBG", []controller.RawPayload{{BatchKey: "volcanic_reports", Raw: []byte("[" + strings.Join(records, ",") + "]")}}); err != nil {
		t.Fatal(err)
	}
}

func runInbox(t *testing.T, ctrl *controller.Controller) int {
	t.Helper()
	changed, err := ctrl.ProcessInbox(context.Background(), "PVMBG", 100)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func TestInboxCaptureSurvivesOwnerLossAndDeduplicatesReplay(t *testing.T) {
	ctrl := pollController(t)
	ctx := httpkit.WithCorrelationID(context.Background(), "inbox-recovery")
	old, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	raw := []byte("[ " + m2VolcanicReport + " ]")
	if err := old.Controller.CaptureRawPoll(ctx, "PVMBG", []controller.RawPayload{{BatchKey: "volcanic_reports", Raw: raw}}); err != nil {
		t.Fatal(err)
	}
	var saved model.InboxMessage
	if err := ctrl.DB.First(&saved).Error; err != nil || !bytes.Equal(saved.Raw, raw) || saved.Status != "retrying" || saved.CorrelationID != "inbox-recovery" {
		t.Fatalf("raw capture lost: %+v %v", saved, err)
	}
	var hazards int64
	ctrl.DB.Model(&model.HazardEvent{}).Count(&hazards)
	if hazards != 0 {
		t.Fatal("capture processed data before commit")
	}
	old.Close()
	next, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if changed := runInbox(t, next.Controller); changed != 1 {
		t.Fatalf("recovery changed=%d", changed)
	}
	if err := next.Controller.CaptureRawPoll(ctx, "PVMBG", []controller.RawPayload{{BatchKey: "volcanic_reports", Raw: raw}}); err != nil {
		t.Fatal(err)
	}
	if changed := runInbox(t, next.Controller); changed != 0 {
		t.Fatal("duplicate capture changed event")
	}
	var outboxes int64
	if err := ctrl.DB.Model(&model.HazardOutbox{}).Count(&outboxes).Error; err != nil || outboxes != 1 {
		t.Fatalf("duplicate effect: %d %v", outboxes, err)
	}
	page, err := ctrl.ListUnprocessed(ctx, "PVMBG", 100, "")
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("processed items remain: %+v %v", page, err)
	}
	var retained int64
	if err := ctrl.DB.Model(&model.InboxMessage{}).Where("status = 'processed' AND octet_length(raw) > 0").Count(&retained).Error; err != nil || retained != 0 {
		t.Fatalf("processed payloads retained on coordinator: %d %v", retained, err)
	}
}

func TestInboxCitusDistributedCommitAndLocalState(t *testing.T) {
	if os.Getenv("AAT_TEST_CITUS") != "1" {
		t.Skip("set AAT_TEST_CITUS=1 with a three-worker Citus test formation")
	}
	ctrl := pollController(t)
	ctx := context.Background()
	if err := storage.EnableCitus(ctx, ctrl.DB); err != nil {
		t.Fatal(err)
	}
	owner, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	// Verify atomic distributed/local effects below, rather than depending on
	// the removed multi_shard_commit_protocol setting.
	captureReports(t, owner.Controller, m2VolcanicReport)
	if _, err := owner.Controller.ProcessInbox(ctx, "PVMBG", 1); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.DB.Exec("ALTER TABLE inbox_messages ADD CONSTRAINT reject_processed CHECK (kind = 'envelope' OR status <> 'processed')").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Controller.ProcessInbox(ctx, "PVMBG", 1); err == nil {
		t.Fatal("expected local status failure")
	}
	var count int64
	for _, table := range []string{"hazard_events", "hazard_outboxes"} {
		if err := ctrl.DB.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("distributed rollback leaked %s: %d %v", table, count, err)
		}
	}
	if err := ctrl.DB.Exec("ALTER TABLE inbox_messages DROP CONSTRAINT reject_processed").Error; err != nil {
		t.Fatal(err)
	}
	if changed := runInbox(t, owner.Controller); changed != 1 {
		t.Fatalf("distributed write changed=%d", changed)
	}
	if err := ctrl.DB.Raw(`SELECT count(*) FROM pg_dist_partition WHERE logicalrelid IN ('poll_owners'::regclass, 'poll_states'::regclass, 'inbox_messages'::regclass, 'hazard_outboxes'::regclass) AND partmethod = 'h'`).Scan(&count).Error; err != nil || count != 0 {
		t.Fatalf("coordination state was sharded: %d %v", count, err)
	}
}

func TestInboxSchemaReplayPreservesKeyOrderAndOtherKeysContinue(t *testing.T) {
	ctrl := pollController(t)
	ctx := context.Background()
	owner, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	oldVersion := strings.Replace(m2VolcanicReport, `"alert_level"`, `"level"`, 1)
	newVersion := strings.Replace(m2VolcanicReport, `"Siaga"`, `"Awas"`, 1)
	other := strings.Replace(m2VolcanicReport, `"m2-v1"`, `"other-key"`, 1)
	captureReports(t, owner.Controller, oldVersion, newVersion, other)
	if changed := runInbox(t, owner.Controller); changed != 1 {
		t.Fatalf("healthy key blocked: changed=%d", changed)
	}
	page, err := ctrl.ListUnprocessed(ctx, "PVMBG", 1, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Status != "parked" || page.Items[0].Retryable || page.NextCursor == nil {
		t.Fatalf("parked page: %+v %v", page, err)
	}
	head := page.Items[0].MessageID
	tail, err := ctrl.ListUnprocessed(ctx, "PVMBG", 1, *page.NextCursor)
	if err != nil || len(tail.Items) != 1 || tail.Items[0].Status != "retrying" || !tail.Items[0].Retryable || tail.NextCursor != nil {
		t.Fatalf("blocked next version: %+v %v", tail, err)
	}
	if _, err := ctrl.ListUnprocessed(ctx, "BMKG", 1, *page.NextCursor); err == nil {
		t.Fatal("cursor allowed changed source")
	}
	if changed := runInbox(t, owner.Controller); changed != 0 {
		t.Fatal("new version bypassed parked predecessor")
	}
	owner.Close()
	ctrl.AcceptPVMBGV2 = true
	updated, err := ctrl.AcquirePollSession(ctx, "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer updated.Close()
	if err := ctrl.ReplayInbox(ctx, head); err != nil {
		t.Fatal(err)
	}
	if changed := runInbox(t, updated.Controller); changed != 2 {
		t.Fatalf("replay changed=%d", changed)
	}
	var event model.HazardEvent
	if err := ctrl.DB.Take(&event, "source_ref_id = ?", "m2-v1").Error; err != nil || event.Severity != "AWAS" {
		t.Fatalf("versions applied out of order: %+v %v", event, err)
	}
	var outbox []model.HazardOutbox
	if err := ctrl.DB.Order("id").Find(&outbox).Error; err != nil {
		t.Fatal(err)
	}
	levels := []string{}
	for _, item := range outbox {
		var hazard model.HazardEvent
		if err := json.Unmarshal(item.Payload, &hazard); err != nil {
			t.Fatal(err)
		}
		if hazard.Ref == "m2-v1" {
			levels = append(levels, hazard.Severity)
		}
	}
	if fmt.Sprint(levels) != "[SIAGA AWAS]" {
		t.Fatalf("replay sequence: %v", levels)
	}
	page, err = ctrl.ListUnprocessed(ctx, "PVMBG", 100, "")
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("recovered items remain: %+v %v", page, err)
	}
	if err := ctrl.ReplayInbox(ctx, head); err != controller.ErrInboxProcessed {
		t.Fatalf("processed message replay allowed: %v", err)
	}
}

func TestInboxMalformedPayloadsAreRetainedWithoutBlockingValidMessages(t *testing.T) {
	ctrl := pollController(t)
	owner, err := ctrl.AcquirePollSession(context.Background(), "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	payloads := [][]byte{
		[]byte(`[{"broken":`),
		[]byte("[\xff]"),
		[]byte(strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65)),
		[]byte(strings.Repeat(" ", controller.MaxProcessBytes+1)),
	}
	for _, raw := range payloads {
		if err := owner.Controller.CaptureRawPoll(context.Background(), "PVMBG", []controller.RawPayload{{BatchKey: "volcanic_reports", Raw: raw}}); err != nil {
			t.Fatal(err)
		}
	}
	captureReports(t, owner.Controller, m2VolcanicReport)
	if changed := runInbox(t, owner.Controller); changed != 1 {
		t.Fatalf("bad payload blocked valid record: %d", changed)
	}
	page, err := ctrl.ListUnprocessed(context.Background(), "PVMBG", 100, "")
	if err != nil || len(page.Items) != len(payloads) {
		t.Fatalf("quarantine: %+v %v", page, err)
	}
	for i, item := range page.Items {
		var message model.InboxMessage
		if err := ctrl.DB.Take(&message, "message_id = ?", item.MessageID).Error; err != nil || !bytes.Equal(message.Raw, payloads[i]) || item.Status != "parked" || item.Attempts != 1 || item.Reason == "" {
			t.Fatalf("raw bytes or reason lost: %+v %v", item, err)
		}
	}
}

func TestInboxEffectsRollbackWhenProcessedFlagCannotCommit(t *testing.T) {
	ctrl := pollController(t)
	owner, err := ctrl.AcquirePollSession(context.Background(), "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	captureReports(t, owner.Controller, m2VolcanicReport)
	if _, err := owner.Controller.ProcessInbox(context.Background(), "PVMBG", 1); err != nil {
		t.Fatal(err)
	}
	if err := ctrl.DB.Exec("ALTER TABLE inbox_messages ADD CONSTRAINT reject_processed CHECK (kind = 'envelope' OR status <> 'processed')").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Controller.ProcessInbox(context.Background(), "PVMBG", 1); err == nil {
		t.Fatal("injected flag failure did not fail transaction")
	}
	for _, table := range []string{"hazard_events", "hazard_outboxes"} {
		var count int64
		if err := ctrl.DB.Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("%s survived rollback: %d %v", table, count, err)
		}
	}
	if err := ctrl.DB.Exec("ALTER TABLE inbox_messages DROP CONSTRAINT reject_processed").Error; err != nil {
		t.Fatal(err)
	}
	if changed := runInbox(t, owner.Controller); changed != 1 {
		t.Fatalf("failed record did not recover: %d", changed)
	}
}

func TestInboxTransientFailureRetriesAfterBackoff(t *testing.T) {
	ctrl := pollController(t)
	owner, err := ctrl.AcquirePollSession(context.Background(), "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	captureReports(t, owner.Controller, m2VolcanicReport)
	if err := ctrl.DB.Exec(`CREATE FUNCTION fail_hazard_write() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'temporary failure' USING ERRCODE = '40001'; END $$ LANGUAGE plpgsql`).Error; err != nil {
		t.Fatal(err)
	}
	if err := ctrl.DB.Exec(`CREATE TRIGGER fail_hazard BEFORE INSERT ON hazard_events FOR EACH ROW EXECUTE FUNCTION fail_hazard_write()`).Error; err != nil {
		t.Fatal(err)
	}
	if changed := runInbox(t, owner.Controller); changed != 0 {
		t.Fatal("failed write reported success")
	}
	var message model.InboxMessage
	if err := ctrl.DB.Take(&message, "kind = 'record'").Error; err != nil || message.Status != "retrying" || message.Attempts != 1 || message.Reason != "processing_unavailable" {
		t.Fatalf("transient error not retryable: %+v %v", message, err)
	}
	if wait := time.Until(message.NextAttemptAt); wait < time.Second || wait > 3*time.Second {
		t.Fatalf("unexpected retry delay: %s", wait)
	}
	if err := ctrl.DB.Exec("DROP TRIGGER fail_hazard ON hazard_events").Error; err != nil {
		t.Fatal(err)
	}
	if err := ctrl.DB.Model(&message).Update("next_attempt_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if changed := runInbox(t, owner.Controller); changed != 1 {
		t.Fatalf("transient recovery changed=%d", changed)
	}
}

func TestInboxAuditRequiresAuditTokenAndOmitsRawBytes(t *testing.T) {
	ctrl := pollController(t)
	owner, err := ctrl.AcquirePollSession(context.Background(), "PVMBG")
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	captureReports(t, owner.Controller, strings.Replace(m2VolcanicReport, `"alert_level"`, `"level"`, 1))
	runInbox(t, owner.Controller)
	handler := aggregator.NewHandlerWithAudit(ctrl, "app-token", "audit-token")
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {"app-token", 401}, {"audit-token", 200}} {
		request := httptest.NewRequest("GET", "/internal/audit/unprocessed?source=PVMBG", nil)
		request.Header.Set("Authorization", "Bearer "+tc.token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("token boundary: %d %s", response.Code, response.Body.String())
		}
		if tc.status == 200 && (strings.Contains(response.Body.String(), "raw_base64") || !strings.Contains(response.Body.String(), "parked")) {
			t.Fatal("audit leaked raw data or omitted state")
		}
	}
}
