package controller

import (
	"context"
	"time"

	"aat/aggregator/internal/dbtrace"
	"aat/aggregator/internal/model"
	"aat/aggregator/internal/storage"
	"gorm.io/gorm"
)

type Controller struct {
	DB              *gorm.DB
	Store           storage.Reader
	ReadConcurrency int
	// ReadPriorityConcurrency read slots are only for reads marked X-Read-Class: priority.
	ReadPriorityConcurrency int
	ReadRPSLimit            int
	ReadRPSBurst            int
	PageCache               *HazardPageCache
	// Set only when DB uses the owner's connection.
	PollOwner     *model.PollOwner
	AcceptPVMBGV2 bool
	// SatelliteLateness overrides DefaultSatelliteLateness when positive.
	SatelliteLateness           time.Duration
	DatabaseRole                string
	VerificationCrash           func()
	SatelliteVerificationSchema string
}

func (ctrl *Controller) Ping(ctx context.Context) error {
	db, err := ctrl.DB.DB()
	if err != nil {
		return err
	}
	return dbtrace.Observe(ctx, "ping", db.PingContext)
}

// HazardStore permits alternate canonical read backends while retaining the
// PostgreSQL adapter for existing callers and integration fixtures.
func (ctrl *Controller) HazardStore() storage.Reader {
	if ctrl.Store != nil {
		return ctrl.Store
	}
	return storage.NewPostgres(ctrl.DB, storage.Identity{Unit: "postgres"})
}
