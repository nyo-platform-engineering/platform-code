package model

import "time"

type PollState struct {
	Source  string `json:"source" gorm:"type:text;primaryKey;check:source_polls_source_check,source IN ('BMKG','PVMBG')"`
	Healthy bool   `json:"healthy" gorm:"not null;default:false"`
	// Next poll includes records at this source timestamp.
	FetchCursor *time.Time `json:"-" gorm:"column:fetch_cursor"`
	// Coordinated polls use database time for last_success_at.
	LastSuccessCallerTime *time.Time `json:"last_success_at" gorm:"column:last_success_at"`
	// Caller time when the failed poll started.
	LastErrorCallerTime *time.Time `json:"-" gorm:"column:last_error_at"`
	LastError           *string    `json:"-" gorm:"type:text"`
}
