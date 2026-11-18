package model

// PollOwner records the session allowed to poll a source. Each takeover increases Epoch.
type PollOwner struct {
	Source     string `gorm:"type:text;primaryKey"`
	Epoch      int64  `gorm:"not null"`
	Owner      string `gorm:"type:text;not null"`
	BackendPID int    `gorm:"column:backend_pid;not null"`
}
