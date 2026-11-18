package model

import "time"

// AI-assisted implementation.
type InboxMessage struct {
	Sequence      int64     `gorm:"primaryKey;autoIncrement" json:"-"`
	MessageID     string    `gorm:"type:text;not null;uniqueIndex" json:"message_id"`
	Source        string    `gorm:"type:text;not null;index:inbox_ready,priority:1;index:inbox_order,priority:1" json:"source"`
	BatchKey      string    `gorm:"type:text;not null" json:"batch_key"`
	Kind          string    `gorm:"type:text;not null;check:inbox_kind_check,kind IN ('envelope','record')" json:"kind"`
	SourceRefID   string    `gorm:"type:text;not null;index:inbox_order,priority:2" json:"source_ref_id"`
	Raw           []byte    `gorm:"type:bytea;not null" json:"raw_base64"`
	ReceivedAt    time.Time `gorm:"not null;default:clock_timestamp()" json:"received_at"`
	Status        string    `gorm:"type:text;not null;index:inbox_ready,priority:2;check:inbox_status_check,status IN ('retrying','parked','processed')" json:"status"`
	Reason        string    `gorm:"type:text;not null" json:"reason"`
	Attempts      int       `gorm:"not null" json:"attempts"`
	NextAttemptAt time.Time `gorm:"not null;default:clock_timestamp();index:inbox_ready,priority:3" json:"-"`
	CorrelationID string    `gorm:"type:text;not null" json:"correlation_id"`
	TraceParent   string    `gorm:"type:text;not null" json:"-"`
	TraceState    string    `gorm:"type:text;not null" json:"-"`
}
