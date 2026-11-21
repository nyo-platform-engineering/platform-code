package model

import "time"

// SatelliteDetection is one VIIRS hotspot. FireRef is set exactly when the
// detection counts toward a fire (not late, not retracted, confidence not low);
// a fire is every detection sharing one FireRef.
//
// detections_neighbours serves the clustering's neighbour search (a latitude band and a
// time window, among detections in a fire): one index range instead of intersecting
// the latitude and time indexes, about 5x less work per detection at 1M rows.
type SatelliteDetection struct {
	Key        string    `gorm:"column:detection_key;type:text;primaryKey"`
	Satellite  string    `gorm:"type:text;not null"`
	DetectedAt time.Time `gorm:"not null;index:detections_time;index:detections_neighbours,priority:2"`
	Latitude   float64   `gorm:"type:double precision;not null;index:detections_latitude;index:detections_neighbours,priority:1,where:fire_ref IS NOT NULL"`
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

// SatelliteFeed is the resume point of the satellite stream. Offset is written in
// the same transaction as the detections it covers, so a crash can never save one
// without the other.
type SatelliteFeed struct {
	Source       string     `gorm:"type:text;primaryKey"`
	Offset       int64      `gorm:"column:feed_offset;not null"`
	LatestOffset *int64     `gorm:"column:latest_offset"`
	VirtualNow   *time.Time `gorm:"column:virtual_now"`
	CheckedAt    *time.Time `gorm:"column:checked_at"`
	UpdatedAt    time.Time  `gorm:"not null"`
}

// SatelliteReject keeps a feed event the mapper could not read, with the reason,
// so it is reported instead of silently dropped.
type SatelliteReject struct {
	Offset     int64     `gorm:"column:feed_offset;primaryKey"`
	EventType  string    `gorm:"type:text;not null"`
	Payload    string    `gorm:"type:text;not null"`
	Reason     string    `gorm:"type:text;not null"`
	ReceivedAt time.Time `gorm:"not null"`
}
