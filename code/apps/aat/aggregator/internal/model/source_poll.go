package model

import "time"

type PollState struct {
	Source  string `json:"source" gorm:"type:text;primaryKey;check:source_polls_source_check,source IN ('BMKG','PVMBG')"`
	Healthy bool   `json:"healthy" gorm:"not null;default:false"`
	// Caller time captured before the last successful poll; used as the next since.
	LastSuccessCallerTime *time.Time `json:"last_success_at" gorm:"column:last_success_at"`
	// Caller time captured before the last failed poll, rather than when its error was recorded.
	LastErrorCallerTime *time.Time `json:"-" gorm:"column:last_error_at"`
	// Error from that failed poll; both error fields are cleared after a successful poll.
	LastError *string `json:"-" gorm:"type:text"`
}
