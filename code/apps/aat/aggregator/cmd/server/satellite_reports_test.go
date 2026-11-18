package main

import (
	"context"
	"testing"
	"time"

	"aat/aggregator/internal/controller"
)

func TestSatelliteFeedAndFieldReportsShareStorage(t *testing.T) {
	ctrl := pollController(t)
	ctx := context.Background()
	owner, err := ctrl.AcquirePollSession(ctx, controller.SatelliteSource)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	result, err := owner.Controller.IngestFeedBatch(ctx, []controller.FeedEvent{{Offset: 7, Type: "heartbeat", ReceivedAt: time.Now().UTC()}})
	if err != nil || result.Offset != 7 {
		t.Fatalf("satellite progress: %+v %v", result, err)
	}
	receipt := acceptReportHTTP(t, ctrl, "alongside-satellite", fieldReportBody)
	if count, err := ctrl.ProcessFieldReports(ctx, 1); err != nil || count != 1 {
		t.Fatalf("field report processing: %d %v", count, err)
	}
	offset, err := owner.Controller.SatelliteFeedOffset(ctx)
	if err != nil || offset == nil || *offset != 7 || receipt.ReportID == "" {
		t.Fatalf("satellite progress lost after report processing: %v %v", offset, err)
	}
}
