package model

// store this to correlate with the record in the database
// e.g. seismic received but tsunami not yet, so when tsunami received, correlate with SeismicEvent in db

// no need for VolcanicReport since it goes directly to HazardEvent

type SeismicEvent struct {
	ID       string `gorm:"type:text;primaryKey"`
	Document Record `gorm:"type:jsonb;serializer:json;not null"`
}

type TsunamiWarning struct {
	ID       string `gorm:"type:text;primaryKey"`
	EventID  string `gorm:"type:text;not null;index:warnings_event"`
	Document Record `gorm:"type:jsonb;serializer:json;not null"`
}
