package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	aggregator "aat/aggregator/internal"
	"aat/aggregator/internal/controller"
	"aat/aggregator/internal/model"
	"aat/internal/fieldreport"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const fieldReportBody = `{"area_name":" Padang ","description":" Jalan terputus ","observed_at":"2026-11-18T08:00:00+07:00"}`

func acceptReportHTTP(t *testing.T, ctrl *controller.Controller, key, body string) fieldreport.Receipt {
	t.Helper()
	handler := aggregator.NewHandler(ctrl, "service-token")
	request := httptest.NewRequest("POST", "/internal/field-reports", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer service-token")
	request.Header.Set("X-Client-ID", "responder")
	request.Header.Set("Idempotency-Key", key)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	var receipt fieldreport.Receipt
	if result.Code != 202 || json.Unmarshal(result.Body.Bytes(), &receipt) != nil {
		t.Fatalf("report not accepted: %d %s", result.Code, result.Body.String())
	}
	return receipt
}

func TestFieldReportAcceptanceIsDurableAndIdempotent(t *testing.T) {
	ctrl := pollController(t)
	first := acceptReportHTTP(t, ctrl, "key-1", fieldReportBody)
	var queued model.FieldReportSubmission
	if err := ctrl.DB.Take(&queued, "id = ?", first.ReportID).Error; err != nil || queued.ProcessedAt != nil || string(queued.Raw) != fieldReportBody {
		t.Fatalf("202 did not persist original report: %+v %v", queued, err)
	}
	retry := acceptReportHTTP(t, ctrl, "key-1", `{ "description":"Jalan terputus", "observed_at":"2026-11-18T01:00:00Z", "area_name":"Padang" }`)
	if retry.ReportID != first.ReportID || !retry.AcceptedAt.Equal(first.AcceptedAt) {
		t.Fatal("equivalent retry changed receipt")
	}
}

func TestFieldReportConcurrentRetriesProduceOneResult(t *testing.T) {
	ctrl := pollController(t)
	ctx := context.Background()
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			receipt, err := ctrl.AcceptFieldReport(ctx, "responder", "same-key", []byte(fieldReportBody))
			if err != nil || receipt.ReportID != fieldreport.ID("responder", "same-key") {
				t.Errorf("retry: %+v %v", receipt, err)
			}
		})
	}
	group.Wait()
	var count int64
	if err := ctrl.DB.Model(&model.FieldReportSubmission{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicate submissions: %d %v", count, err)
	}
	if _, err := ctrl.AcceptFieldReport(ctx, "responder", "same-key", []byte(strings.Replace(fieldReportBody, "Jalan terputus", "Butuh perahu", 1))); err != controller.ErrReportConflict {
		t.Fatalf("key reuse accepted different body: %v", err)
	}
	for range 8 {
		group.Go(func() {
			if _, err := ctrl.ProcessFieldReports(ctx, 32); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if err := ctrl.DB.Model(&model.FieldReport{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicate processing effect: %d %v", count, err)
	}
	if changed, err := ctrl.ProcessFieldReports(ctx, 32); err != nil || changed != 0 {
		t.Fatalf("completed report processed twice: %d %v", changed, err)
	}
	var report model.FieldReport
	if err := ctrl.DB.First(&report).Error; err != nil {
		t.Fatal(err)
	}
	var normalized fieldreport.Submission
	if err := json.Unmarshal(report.Document, &normalized); err != nil || normalized.AreaName != "Padang" || normalized.Description != "Jalan terputus" || normalized.ObservedAt.Format(time.RFC3339) != "2026-11-18T01:00:00Z" {
		t.Fatalf("normalization missing: %+v %v", normalized, err)
	}
}

func TestFieldReportProcessingRollsBackWithCompletionFlag(t *testing.T) {
	ctrl := pollController(t)
	receipt := acceptReportHTTP(t, ctrl, "rollback", fieldReportBody)
	if err := ctrl.DB.Exec("ALTER TABLE field_report_submissions ADD CONSTRAINT reject_report_completion CHECK (processed_at IS NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.ProcessFieldReports(context.Background(), 1); err == nil {
		t.Fatal("expected completion failure")
	}
	var count int64
	if err := ctrl.DB.Model(&model.FieldReport{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("effect survived failed completion: %d %v", count, err)
	}
	var queued model.FieldReportSubmission
	if err := ctrl.DB.Take(&queued, "id = ?", receipt.ReportID).Error; err != nil || queued.ProcessedAt != nil || len(queued.Raw) == 0 {
		t.Fatalf("pending report lost: %+v %v", queued, err)
	}
	if err := ctrl.DB.Exec("ALTER TABLE field_report_submissions DROP CONSTRAINT reject_report_completion").Error; err != nil {
		t.Fatal(err)
	}
	if count, err := ctrl.ProcessFieldReports(context.Background(), 1); err != nil || count != 1 {
		t.Fatalf("retry failed: %d %v", count, err)
	}
	if err := ctrl.DB.Take(&queued, "id = ?", receipt.ReportID).Error; err != nil || queued.ProcessedAt == nil || len(queued.Raw) != 0 {
		t.Fatalf("completion not committed: %+v %v", queued, err)
	}
	retry := acceptReportHTTP(t, ctrl, "rollback", fieldReportBody)
	if retry.ReportID != receipt.ReportID || !retry.AcceptedAt.Equal(receipt.AcceptedAt) {
		t.Fatal("retry changed original receipt")
	}
	handler := aggregator.NewHandler(ctrl, "service-token")
	request := httptest.NewRequest("GET", "/internal/ops/field-reports/"+receipt.ReportID, nil)
	request.Header.Set("Authorization", "Bearer service-token")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != 200 || !strings.Contains(result.Body.String(), `"status":"processed"`) || !strings.Contains(result.Body.String(), `"description":"Jalan terputus"`) {
		t.Fatalf("processed report unavailable to Ops: %d %s", result.Code, result.Body.String())
	}
}

func TestFieldReportAcceptanceDoesNotWaitForBlockedProcessor(t *testing.T) {
	ctrl := pollController(t)
	acceptReportHTTP(t, ctrl, "blocked-first", fieldReportBody)
	db, err := ctrl.DB.DB()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.Exec("LOCK TABLE field_reports IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	worker := make(chan error, 1)
	go func() { _, err := ctrl.ProcessFieldReports(ctx, 1); worker <- err }()
	acceptReportHTTP(t, ctrl, "accepted-while-blocked", fieldReportBody)
	if err := <-worker; err == nil {
		t.Fatal("processor did not encounter the blocked sink")
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	if count, err := ctrl.ProcessFieldReports(context.Background(), 32); err != nil || count != 2 {
		t.Fatalf("queued reports did not recover: %d %v", count, err)
	}
}

func TestFieldReportStorageFailureDoesNotAcknowledge(t *testing.T) {
	ctrl := pollController(t)
	if err := ctrl.DB.Exec(`CREATE FUNCTION reject_report() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'storage unavailable'; END $$ LANGUAGE plpgsql`).Error; err != nil {
		t.Fatal(err)
	}
	if err := ctrl.DB.Exec(`CREATE TRIGGER reject_report BEFORE INSERT ON field_report_submissions FOR EACH ROW EXECUTE FUNCTION reject_report()`).Error; err != nil {
		t.Fatal(err)
	}
	handler := aggregator.NewHandler(ctrl, "service-token")
	request := httptest.NewRequest("POST", "/internal/field-reports", strings.NewReader(fieldReportBody))
	request.Header.Set("Authorization", "Bearer service-token")
	request.Header.Set("X-Client-ID", "responder")
	request.Header.Set("Idempotency-Key", "no-ack")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != 503 {
		t.Fatalf("failed storage acknowledged: %d %s", result.Code, result.Body.String())
	}
}

func TestFieldReportSurvivesReceiverProcessCrashAfter202(t *testing.T) {
	ctrl := pollController(t)
	var schema string
	if err := ctrl.DB.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFieldReportCrashHelper$")
	cmd.Env = append(os.Environ(), "AAT_FIELD_REPORT_CHILD=1", "AAT_FIELD_REPORT_SCHEMA="+schema)
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "REPORT_URL=") {
				ready <- strings.TrimPrefix(scanner.Text(), "REPORT_URL=")
				return
			}
		}
		ready <- ""
	}()
	var endpoint string
	select {
	case endpoint = <-ready:
	case <-ctx.Done():
		t.Fatal("receiver did not start")
	}
	if endpoint == "" {
		t.Fatal("receiver exited before startup")
	}
	request, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/internal/field-reports", strings.NewReader(fieldReportBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer service-token")
	request.Header.Set("X-Client-ID", "responder")
	request.Header.Set("Idempotency-Key", "crash-after-202")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var receipt fieldreport.Receipt
	err = json.NewDecoder(response.Body).Decode(&receipt)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != 202 {
		t.Fatalf("receiver did not acknowledge: %d %v", response.StatusCode, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	restarted := &controller.Controller{DB: ctrl.DB}
	if count, err := restarted.ProcessFieldReports(ctx, 32); err != nil || count != 1 {
		t.Fatalf("report lost after receiver crash: %d %v", count, err)
	}
	retry := acceptReportHTTP(t, restarted, "crash-after-202", fieldReportBody)
	if retry.ReportID != receipt.ReportID || !retry.AcceptedAt.Equal(receipt.AcceptedAt) {
		t.Fatal("crash recovery changed receipt")
	}
	if count, err := restarted.ProcessFieldReports(ctx, 32); err != nil || count != 0 {
		t.Fatalf("retry duplicated crash result: %d %v", count, err)
	}
}

func TestFieldReportCrashHelper(t *testing.T) {
	if os.Getenv("AAT_FIELD_REPORT_CHILD") != "1" {
		t.Skip("child process helper")
	}
	gin.SetMode(gin.ReleaseMode)
	config, err := pgx.ParseConfig(os.Getenv("AAT_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = os.Getenv("AAT_FIELD_REPORT_SCHEMA")
	db := stdlib.OpenDB(*config)
	defer db.Close()
	store, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(aggregator.NewHandler(&controller.Controller{DB: store}, "service-token"))
	defer server.Close()
	fmt.Fprintln(os.Stdout, "REPORT_URL="+server.URL)
	time.Sleep(30 * time.Second)
}
