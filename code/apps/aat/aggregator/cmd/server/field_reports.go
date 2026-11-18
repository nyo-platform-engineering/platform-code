package main

import (
	"context"
	"log/slog"
	"time"

	"aat/aggregator/internal/controller"
)

func runFieldReportProcessor(ctx context.Context, ctrl *controller.Controller) {
	for ctx.Err() == nil {
		processCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		count, err := ctrl.ProcessFieldReports(processCtx, 32)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("field report processing will retry", "error", err)
		}
		if count > 0 {
			slog.Info("field reports processed", "count", count)
		}
		if !wait(ctx, 500*time.Millisecond) {
			return
		}
	}
}
