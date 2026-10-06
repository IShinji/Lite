package models

import "time"

const (
	ScheduledExecReasonSecurity  = "security"
	ScheduledExecReasonRemoteOff = "remote_off"

	ScheduledExecRunDispatched = "dispatched"
	ScheduledExecRunSkipped    = "skipped"
	ScheduledExecRunFailed     = "failed"
)

// ScheduledExec is an administrator-defined command that runs on a schedule.
// Enabled is cleared when remote management is turned off or the owner's
// credentials change, so a stored command cannot keep running on its own.
type ScheduledExec struct {
	ID              string      `json:"id" gorm:"type:varchar(32);primaryKey"`
	Name            string      `json:"name" gorm:"type:varchar(64);not null"`
	Command         string      `json:"command" gorm:"type:text;not null"`
	Clients         StringArray `json:"clients" gorm:"type:longtext"`
	OwnerUserUUID   string      `json:"-" gorm:"type:varchar(36);index;not null"`
	Enabled         bool        `json:"enabled" gorm:"index;not null"`
	DisabledReason  string      `json:"disabled_reason" gorm:"type:varchar(32)"`
	Kind            string      `json:"kind" gorm:"type:varchar(16);not null"`
	IntervalMinutes int         `json:"interval_minutes"`
	TimeOfDay       string      `json:"time_of_day" gorm:"type:varchar(5)"`
	Weekday         int         `json:"weekday"`
	Weekdays        string      `json:"weekdays" gorm:"type:varchar(32)"`
	MonthDay        int         `json:"month_day"`
	LastRunAt       *time.Time  `json:"last_run_at" gorm:"type:timestamp"`
	NextRunAt       *time.Time  `json:"next_run_at" gorm:"type:timestamp;index"`
	SortOrder       int         `json:"-" gorm:"not null;default:0;index"`
	CreatedAt       time.Time   `json:"created_at" gorm:"type:timestamp"`
	UpdatedAt       time.Time   `json:"updated_at" gorm:"type:timestamp"`
}

// ScheduledExecRun is one attempt. TaskID points at the exec task that holds
// each server's output. A run with an empty TaskID did not send a command.
type ScheduledExecRun struct {
	ID         string    `json:"id" gorm:"type:varchar(32);primaryKey"`
	ScheduleID string    `json:"schedule_id" gorm:"type:varchar(32);index;not null"`
	TaskID     string    `json:"task_id" gorm:"type:varchar(36);index"`
	Status     string    `json:"status" gorm:"type:varchar(16);not null"`
	Note       string    `json:"note" gorm:"type:varchar(32)"`
	Sent       int       `json:"sent"`
	Queued     int       `json:"queued"`
	Offline    int       `json:"offline"`
	Failed     int       `json:"failed"`
	StartedAt  time.Time `json:"started_at" gorm:"type:timestamp;index"`
}
