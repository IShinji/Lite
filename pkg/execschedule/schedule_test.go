package execschedule

import (
	"errors"
	"testing"
	"time"
)

func TestValidateInterval(t *testing.T) {
	spec, err := Validate(KindInterval, 15, "09:00", 3)
	if err != nil {
		t.Fatal(err)
	}
	if spec.IntervalMinutes != 15 || spec.TimeOfDay != "" || spec.Weekday != 0 {
		t.Fatalf("spec = %+v", spec)
	}
	if _, err := Validate(KindInterval, 0, "", 0); !errors.Is(err, ErrInterval) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Validate(KindInterval, MaxIntervalMinutes+1, "", 0); !errors.Is(err, ErrInterval) {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateClock(t *testing.T) {
	spec, err := Validate(KindDaily, 0, "09:05", 4)
	if err != nil {
		t.Fatal(err)
	}
	if spec.TimeOfDay != "09:05" || spec.Weekday != 0 {
		t.Fatalf("spec = %+v", spec)
	}
	weekly, err := Validate(KindWeekly, 0, "23:59", int(time.Monday))
	if err != nil {
		t.Fatal(err)
	}
	if weekly.Weekday != int(time.Monday) {
		t.Fatalf("weekday = %d", weekly.Weekday)
	}
	if _, err := Validate(KindDaily, 0, "9:00", 0); !errors.Is(err, ErrTime) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Validate(KindWeekly, 0, "09:00", 7); !errors.Is(err, ErrWeekday) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Validate("cron", 1, "", 0); !errors.Is(err, ErrInvalidSchedule) {
		t.Fatalf("err = %v", err)
	}
}

func TestNextUsesBeijingClock(t *testing.T) {
	loc := time.FixedZone("Asia/Shanghai", 8*60*60)
	after := time.Date(2026, 10, 6, 10, 0, 0, 0, loc)

	next, err := Next(Spec{Kind: KindInterval, IntervalMinutes: 15}, after, loc)
	if err != nil {
		t.Fatal(err)
	}
	if want := after.Add(15 * time.Minute); !next.Equal(want) {
		t.Fatalf("interval next = %s, want %s", next, want)
	}

	daily, err := Next(Spec{Kind: KindDaily, TimeOfDay: "09:00"}, after, loc)
	if err != nil {
		t.Fatal(err)
	}
	wantDaily := time.Date(2026, 10, 7, 9, 0, 0, 0, loc)
	if !daily.Equal(wantDaily) {
		t.Fatalf("daily next = %s, want %s", daily, wantDaily)
	}

	laterToday, err := Next(Spec{Kind: KindDaily, TimeOfDay: "11:30"}, after, loc)
	if err != nil {
		t.Fatal(err)
	}
	wantLater := time.Date(2026, 10, 6, 11, 30, 0, 0, loc)
	if !laterToday.Equal(wantLater) {
		t.Fatalf("later today = %s, want %s", laterToday, wantLater)
	}

	// 2026-10-06 is a Tuesday. Next Monday 09:00 is the 12th.
	weekly, err := Next(Spec{Kind: KindWeekly, TimeOfDay: "09:00", Weekday: int(time.Monday)}, after, loc)
	if err != nil {
		t.Fatal(err)
	}
	wantWeek := time.Date(2026, 10, 12, 9, 0, 0, 0, loc)
	if !weekly.Equal(wantWeek) {
		t.Fatalf("weekly next = %s, want %s", weekly, wantWeek)
	}

	sameDayLater, err := Next(Spec{Kind: KindWeekly, TimeOfDay: "18:00", Weekday: int(time.Tuesday)}, after, loc)
	if err != nil {
		t.Fatal(err)
	}
	wantSame := time.Date(2026, 10, 6, 18, 0, 0, 0, loc)
	if !sameDayLater.Equal(wantSame) {
		t.Fatalf("same weekday = %s, want %s", sameDayLater, wantSame)
	}

	several, err := Next(Spec{Kind: KindWeekly, TimeOfDay: "09:00", Weekdays: []int{int(time.Monday), int(time.Wednesday)}}, after, loc)
	if err != nil {
		t.Fatal(err)
	}
	wantSeveral := time.Date(2026, 10, 7, 9, 0, 0, 0, loc)
	if !several.Equal(wantSeveral) {
		t.Fatalf("several weekdays = %s, want %s", several, wantSeveral)
	}

	monthly, err := Next(Spec{Kind: KindMonthly, TimeOfDay: "03:00", MonthDay: 31}, after, loc)
	if err != nil {
		t.Fatal(err)
	}
	wantMonth := time.Date(2026, 10, 31, 3, 0, 0, 0, loc)
	if !monthly.Equal(wantMonth) {
		t.Fatalf("monthly next = %s, want %s", monthly, wantMonth)
	}

	shortMonth, err := Next(Spec{Kind: KindMonthly, TimeOfDay: "03:00", MonthDay: 31}, time.Date(2026, 10, 31, 3, 0, 0, 0, loc), loc)
	if err != nil {
		t.Fatal(err)
	}
	wantShort := time.Date(2026, 11, 30, 3, 0, 0, 0, loc)
	if !shortMonth.Equal(wantShort) {
		t.Fatalf("short month = %s, want %s", shortMonth, wantShort)
	}
}

func TestNormalizeWeekdaysAndMonthDay(t *testing.T) {
	spec, err := Normalize(Spec{Kind: KindWeekly, TimeOfDay: "08:00", Weekdays: []int{3, 1, 1}})
	if err != nil {
		t.Fatal(err)
	}
	if FormatWeekdays(spec.Weekdays) != "1,3" || spec.Weekday != 1 {
		t.Fatalf("spec = %+v", spec)
	}
	if _, err := Normalize(Spec{Kind: KindWeekly, TimeOfDay: "08:00", Weekday: 7}); !errors.Is(err, ErrWeekday) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Normalize(Spec{Kind: KindWeekly, TimeOfDay: "08:00", Weekdays: []int{}}); !errors.Is(err, ErrWeekday) {
		t.Fatalf("err = %v", err)
	}
	if _, err := Normalize(Spec{Kind: KindMonthly, TimeOfDay: "08:00", MonthDay: 0}); !errors.Is(err, ErrMonthDay) {
		t.Fatalf("err = %v", err)
	}
	if ParseWeekdays("") != nil {
		t.Fatal("empty weekdays should stay empty")
	}
}

func TestValidateNameAndCommand(t *testing.T) {
	name, err := ValidateName("  备份  ")
	if err != nil || name != "备份" {
		t.Fatalf("name = %q, err = %v", name, err)
	}
	if _, err := ValidateName("   "); !errors.Is(err, ErrNameRequired) {
		t.Fatalf("err = %v", err)
	}
	if err := ValidateCommand("  whoami"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateCommand(" \n\t"); !errors.Is(err, ErrCommandEmpty) {
		t.Fatalf("err = %v", err)
	}
}
