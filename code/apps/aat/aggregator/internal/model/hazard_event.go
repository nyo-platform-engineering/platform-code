package model

import (
	"encoding/json"
	"time"
)

type Record map[string]json.RawMessage

type HazardEvent struct {
	ID         string    `json:"hazard_id" gorm:"column:id;type:text;primaryKey;index:hazards_source,priority:2"`
	Source     string    `json:"source" gorm:"type:text;not null;index:hazards_source,priority:1;uniqueIndex:hazards_source_ref,priority:1;check:hazards_source_check,source IN ('BMKG','PVMBG')"`
	Ref        string    `json:"source_ref_id" gorm:"column:source_ref_id;type:text;not null;uniqueIndex:hazards_source_ref,priority:2"`
	Type       string    `json:"hazard_type" gorm:"column:hazard_type;type:text;not null;check:hazards_hazard_type_check,hazard_type IN ('SEISMIC','VOLCANIC')"`
	Severity   string    `json:"severity" gorm:"type:text;not null;check:hazards_severity_check,severity IN ('NORMAL','WASPADA','SIAGA','AWAS')"`
	Area       string    `json:"area_name" gorm:"column:area_name;type:text;not null"`
	Latitude   float64   `json:"latitude" gorm:"type:double precision;not null;check:hazards_latitude_check,latitude BETWEEN -90 AND 90"`
	Longitude  float64   `json:"longitude" gorm:"type:double precision;not null;check:hazards_longitude_check,longitude BETWEEN -180 AND 180"`
	Occurred   time.Time `json:"occurred_at" gorm:"column:occurred_at;not null"`
	Ingested   time.Time `json:"ingested_at" gorm:"column:ingested_at;not null"`
	Attributes Record    `json:"attributes" gorm:"column:attributes;type:jsonb;serializer:json;not null;check:hazards_attributes_check,jsonb_typeof(attributes) = 'object'"`
}
