// Package execschedule validates remote command schedules and computes the
// next run. Clock times use the caller's location, which Lite fixes to
// Asia/Shanghai so a container timezone cannot move the wall clock.
package execschedule

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	KindInterval = "interval"
	KindDaily    = "daily"
	KindWeekly   = "weekly"
	KindMonthly  = "monthly"

	MinIntervalMinutes = 1
	MaxIntervalMinutes = 30 * 24 * 60
	MaxClients         = 100
	MaxSchedules       = 30
	MaxNameRunes       = 64
	MaxCommandBytes    = 64 << 10
	RunHistory         = 50
)

var (
	ErrNameRequired    = errors.New("scheduled task name is required")
	ErrNameTooLong     = errors.New("scheduled task name is too long")
	ErrCommandEmpty    = errors.New("Command cannot be empty")
	ErrCommandTooLong  = errors.New("Command is too long")
	ErrInvalidSchedule = errors.New("invalid scheduled task")
	ErrInterval        = errors.New("scheduled task interval is invalid")
	ErrTime            = errors.New("scheduled task time is invalid")
	ErrWeekday         = errors.New("scheduled task weekday is invalid")
	ErrMonthDay        = errors.New("scheduled task month day is invalid")
)

// Spec is a validated schedule. Weekday uses time.Weekday, Sunday = 0.
// Weekdays lists every selected day. MonthDay is 1–31 for a monthly schedule.
type Spec struct {
	Kind            string
	IntervalMinutes int
	TimeOfDay       string
	Weekday         int
	Weekdays        []int
	MonthDay        int
}

// Validate checks a single-weekday schedule and returns the normalized spec.
func Validate(kind string, intervalMinutes int, timeOfDay string, weekday int) (Spec, error) {
	return Normalize(Spec{Kind: kind, IntervalMinutes: intervalMinutes, TimeOfDay: timeOfDay, Weekday: weekday})
}

// Normalize checks a schedule, including several weekdays or a day of the month.
func Normalize(spec Spec) (Spec, error) {
	switch spec.Kind {
	case KindInterval:
		if spec.IntervalMinutes < MinIntervalMinutes || spec.IntervalMinutes > MaxIntervalMinutes {
			return Spec{}, ErrInterval
		}
		spec.TimeOfDay = ""
		spec.Weekday = 0
		spec.Weekdays = nil
		spec.MonthDay = 0
	case KindDaily, KindWeekly, KindMonthly:
		parsed, err := parseClock(spec.TimeOfDay)
		if err != nil {
			return Spec{}, err
		}
		spec.TimeOfDay = parsed
		spec.IntervalMinutes = 0
		if spec.Kind == KindDaily {
			spec.Weekday = 0
			spec.Weekdays = nil
			spec.MonthDay = 0
			break
		}
		if spec.Kind == KindMonthly {
			if spec.MonthDay < 1 || spec.MonthDay > 31 {
				return Spec{}, ErrMonthDay
			}
			spec.Weekday = 0
			spec.Weekdays = nil
			break
		}
		days := spec.Weekdays
		if days == nil {
			days = []int{spec.Weekday}
		}
		days, err = normalizeWeekdays(days)
		if err != nil {
			return Spec{}, err
		}
		spec.Weekdays = days
		spec.Weekday = days[0]
		spec.MonthDay = 0
	default:
		return Spec{}, ErrInvalidSchedule
	}
	return spec, nil
}

func normalizeWeekdays(days []int) ([]int, error) {
	seen := make(map[int]struct{}, len(days))
	out := make([]int, 0, len(days))
	for _, day := range days {
		if day < int(time.Sunday) || day > int(time.Saturday) {
			return nil, ErrWeekday
		}
		if _, ok := seen[day]; ok {
			continue
		}
		seen[day] = struct{}{}
		out = append(out, day)
	}
	if len(out) == 0 {
		return nil, ErrWeekday
	}
	sort.Ints(out)
	return out, nil
}

// FormatWeekdays stores selected days as a stable comma-separated list.
func FormatWeekdays(days []int) string {
	if len(days) == 0 {
		return ""
	}
	parts := make([]string, len(days))
	for i, day := range days {
		parts[i] = strconv.Itoa(day)
	}
	return strings.Join(parts, ",")
}

// ParseWeekdays reads a stored weekday list. An empty value means none were stored.
func ParseWeekdays(value string) []int {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		day, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		out = append(out, day)
	}
	return out
}

// ValidateName trims the display name and rejects empty or over-long values.
func ValidateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrNameRequired
	}
	if utf8.RuneCountInString(name) > MaxNameRunes {
		return "", ErrNameTooLong
	}
	return name, nil
}

// ValidateCommand rejects an empty or oversized shell command. The bytes are
// kept as entered so leading spaces remain part of the command.
func ValidateCommand(command string) error {
	if strings.TrimSpace(command) == "" {
		return ErrCommandEmpty
	}
	if len(command) > MaxCommandBytes {
		return ErrCommandTooLong
	}
	return nil
}

// Next returns the first instant strictly after `after`.
// Interval schedules wait one full interval. Clock schedules use loc.
func Next(spec Spec, after time.Time, loc *time.Location) (time.Time, error) {
	checked, err := Normalize(spec)
	if err != nil {
		return time.Time{}, err
	}
	if loc == nil {
		loc = time.UTC
	}
	switch checked.Kind {
	case KindInterval:
		return after.Add(time.Duration(checked.IntervalMinutes) * time.Minute), nil
	case KindDaily:
		hour, minute := clockParts(checked.TimeOfDay)
		local := after.In(loc)
		candidate := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
		if !candidate.After(after) {
			candidate = candidate.AddDate(0, 0, 1)
		}
		return candidate, nil
	case KindMonthly:
		hour, minute := clockParts(checked.TimeOfDay)
		return nextMonthDay(after, loc, checked.MonthDay, hour, minute), nil
	default:
		hour, minute := clockParts(checked.TimeOfDay)
		var soonest time.Time
		for _, day := range checked.Weekdays {
			candidate := nextWeekday(after, loc, day, hour, minute)
			if soonest.IsZero() || candidate.Before(soonest) {
				soonest = candidate
			}
		}
		return soonest, nil
	}
}

func nextWeekday(after time.Time, loc *time.Location, weekday, hour, minute int) time.Time {
	local := after.In(loc)
	candidate := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	delta := (weekday - int(candidate.Weekday()) + 7) % 7
	candidate = candidate.AddDate(0, 0, delta)
	if !candidate.After(after) {
		candidate = candidate.AddDate(0, 0, 7)
	}
	return candidate
}

func nextMonthDay(after time.Time, loc *time.Location, day, hour, minute int) time.Time {
	local := after.In(loc)
	year, month := local.Year(), local.Month()
	for range 14 {
		candidate := time.Date(year, month, clampedMonthDay(year, month, day, loc), hour, minute, 0, 0, loc)
		if candidate.After(after) {
			return candidate
		}
		month++
		if month > 12 {
			month = 1
			year++
		}
	}
	return time.Date(year, month, clampedMonthDay(year, month, day, loc), hour, minute, 0, 0, loc)
}

func clampedMonthDay(year int, month time.Month, day int, loc *time.Location) int {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, loc).Day()
	if day > last {
		return last
	}
	return day
}

func parseClock(value string) (string, error) {
	if len(value) != 5 || value[2] != ':' {
		return "", ErrTime
	}
	hour := int(value[0]-'0')*10 + int(value[1]-'0')
	minute := int(value[3]-'0')*10 + int(value[4]-'0')
	if value[0] < '0' || value[0] > '9' || value[1] < '0' || value[1] > '9' ||
		value[3] < '0' || value[3] > '9' || value[4] < '0' || value[4] > '9' ||
		hour > 23 || minute > 59 {
		return "", ErrTime
	}
	return value, nil
}

func clockParts(value string) (int, int) {
	hour := int(value[0]-'0')*10 + int(value[1]-'0')
	minute := int(value[3]-'0')*10 + int(value[4]-'0')
	return hour, minute
}
