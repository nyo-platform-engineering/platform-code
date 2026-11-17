package model

import "time"

// SatelliteDetection is one VIIRS hotspot. FireRef is set exactly when the
// detection counts toward a fire (not late, not retracted, confidence not low);
// a fire is every detection sharing one FireRef.
type SatelliteDetection struct {
	Key        string    `gorm:"column:detection_key;type:text;primaryKey"`
	Satellite  string    `gorm:"type:text;not null"`
	DetectedAt time.Time `gorm:"not null;index:detections_time"`
	Latitude   float64   `gorm:"type:double precision;not null;index:detections_latitude"`
	Longitude  float64   `gorm:"type:double precision;not null"`
	Confidence string    `gorm:"type:text;not null"`
	FRP        float64   `gorm:"column:frp_mw;type:double precision;not null"`
	Late       bool      `gorm:"not null"`
	Retracted  bool      `gorm:"not null"`
	FireRef    *string   `gorm:"type:text;index:detections_fire"`
	ReceivedAt time.Time `gorm:"not null"`
}

// SatelliteFire records clustering facts that must survive replays: the anchor
// detection a fire's ref is derived from, whether it ever had two detections
// (and so has a HazardEvent), and which fire absorbed it.
type SatelliteFire struct {
	Ref        string  `gorm:"type:text;primaryKey"`
	AnchorKey  string  `gorm:"type:text;not null"`
	Formed     bool    `gorm:"not null"`
	MergedInto *string `gorm:"type:text"`
}
