package models

import "time"

// TrafficBucketLedger is a local-patch table (not upstream). It stores exact
// traffic per 15-minute UTC-aligned bucket so the dashboard can re-slice
// history on the day boundary of any viewer time zone (including +5:30,
// +5:45 and +12:45). BucketStart is a UTC unix second divisible by 900.
// TrafficDailyLedger remains the Beijing-day source for billing and reports.
type TrafficBucketLedger struct {
	Client      string    `json:"client" gorm:"type:varchar(36);primaryKey;not null"`
	ClientInfo  Client    `json:"-" gorm:"foreignKey:Client;references:UUID;constraint:OnDelete:CASCADE,OnUpdate:CASCADE"`
	BucketStart int64     `json:"bucket_start" gorm:"primaryKey;autoIncrement:false;not null;index:idx_traffic_bucket_ledger_start"`
	UpBytes     int64     `json:"up_bytes" gorm:"type:bigint;not null;default:0"`
	DownBytes   int64     `json:"down_bytes" gorm:"type:bigint;not null;default:0"`
	CreatedAt   time.Time `json:"created_at"`
}
