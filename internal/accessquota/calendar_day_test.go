package accessquota

import (
	"testing"
	"time"
)

func calendarRule(id uint, timezone string) Rule {
	return Rule{ID: id, Revision: 1, Kind: KindPeriodic, LimitNanoUSD: 20,
		PeriodSeconds: CalendarDaySeconds, PeriodMode: PeriodModeCalendarDay, Timezone: timezone}
}

func TestCalendarDayResetsAtMidnightAndKeepsDurationIndependent(t *testing.T) {
	t.Parallel()
	rules := map[uint][]Rule{1: {calendarRule(1, DefaultTimezone),
		{ID: 2, Revision: 1, Kind: KindPeriodic, LimitNanoUSD: 100, PeriodSeconds: CalendarDaySeconds}}}
	runtime := NewRuntime()
	if err := runtime.Reconcile(rules); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation(DefaultTimezone)
	start := time.Date(2026, 10, 18, 23, 0, 0, 0, loc)
	midnight := time.Date(2026, 10, 19, 0, 0, 0, 0, loc)
	preview := ruleViewByID(t, runtime.Snapshot(1, start).Rules, 1)
	if preview.Status != RuleStatusAvailable || preview.WindowEndsAtMS == nil || *preview.WindowEndsAtMS != midnight.UnixMilli() {
		t.Fatalf("unused calendar day must show next midnight: %#v", preview)
	}
	ticket, _ := runtime.Admit(1, start)
	runtime.Complete(ticket, 20)
	blocked := runtime.Check(1, midnight.Add(-time.Millisecond))
	if blocked.Allowed || blocked.NextAvailableAtMS == nil || *blocked.NextAvailableAtMS != midnight.UnixMilli() {
		t.Fatalf("calendar-day recovery: %#v", blocked)
	}
	if !runtime.Check(1, midnight).Allowed {
		t.Fatal("calendar day must reset exactly at midnight")
	}
	next, _ := runtime.Admit(1, midnight)
	runtime.Complete(next, 3)
	// A request admitted yesterday can finish late without using today's allowance.
	runtime.Complete(ticket, 7)
	view := runtime.Snapshot(1, midnight)
	if got := ruleViewByID(t, view.Rules, 1).UsedNanoUSD; got != 3 {
		t.Fatalf("calendar usage = %d, want 3", got)
	}
	if got := ruleViewByID(t, view.Rules, 2).UsedNanoUSD; got != 30 {
		t.Fatalf("24-hour usage = %d, want 30", got)
	}
}

func TestCalendarDayRestoreAndManualResetKeepCalendarBoundary(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		date, timezone string
		hours          int
	}{
		{"2026-03-08", "America/New_York", 23},
		{"2026-11-01", "America/New_York", 25},
		{"2026-10-18", DefaultTimezone, 24},
	} {
		t.Run(scenario.date, func(t *testing.T) {
			loc, _ := time.LoadLocation(scenario.timezone)
			day, _ := time.ParseInLocation(time.DateOnly, scenario.date, loc)
			now := day.Add(12 * time.Hour)
			rule := calendarRule(1, scenario.timezone)
			runtime := NewRuntime()
			if err := runtime.Reconcile(map[uint][]Rule{1: {rule}}); err != nil {
				t.Fatal(err)
			}
			ticket, _ := runtime.Admit(1, now)
			runtime.Complete(ticket, 9)
			view := ruleViewByID(t, runtime.Snapshot(1, now).Rules, 1)
			if *view.WindowStartedAtMS != day.UnixMilli() || *view.WindowEndsAtMS != day.AddDate(0, 0, 1).UnixMilli() || *view.WindowEndsAtMS-*view.WindowStartedAtMS != int64(scenario.hours)*time.Hour.Milliseconds() {
				t.Fatalf("incorrect calendar boundary across DST: %#v", view)
			}
			restarted := NewRuntime()
			if err := restarted.Restore(runtime.DirtySnapshots(-1)); err != nil {
				t.Fatal(err)
			}
			if err := restarted.Reconcile(map[uint][]Rule{1: {rule}}); err != nil {
				t.Fatal(err)
			}
			if ruleViewByID(t, restarted.Snapshot(1, now).Rules, 1).UsedNanoUSD != 9 {
				t.Fatal("restart lost usage")
			}
			rule.Revision++ // The control-plane reset clears usage by advancing the revision.
			if err := restarted.Reconcile(map[uint][]Rule{1: {rule}}); err != nil {
				t.Fatal(err)
			}
			if _, decision := restarted.Admit(1, now.Add(time.Minute)); !decision.Allowed {
				t.Fatal("reset did not restore allowance")
			}
			after := ruleViewByID(t, restarted.Snapshot(1, now.Add(time.Minute)).Rules, 1)
			if after.UsedNanoUSD != 0 || *after.WindowEndsAtMS != *view.WindowEndsAtMS {
				t.Fatalf("reset moved midnight: %#v", after)
			}
		})
	}
}

func TestCalendarDayRejectsInvalidDefinitionsAndUnversionedChanges(t *testing.T) {
	t.Parallel()
	for _, mutate := range []func(*Rule){
		func(r *Rule) { r.PeriodSeconds *= 5 },
		func(r *Rule) { r.Timezone = "Local" },
		func(r *Rule) { r.Timezone = "Invalid/Zone" },
		func(r *Rule) { r.Kind = KindTotal; r.PeriodSeconds = 0 },
		func(r *Rule) { r.PeriodMode = "unknown" },
	} {
		rule := calendarRule(1, DefaultTimezone)
		mutate(&rule)
		if err := NewRuntime().Reconcile(map[uint][]Rule{1: {rule}}); err == nil {
			t.Fatalf("accepted invalid rule: %#v", rule)
		}
	}
	runtime := NewRuntime()
	rule := calendarRule(1, DefaultTimezone)
	if err := runtime.Reconcile(map[uint][]Rule{1: {rule}}); err != nil {
		t.Fatal(err)
	}
	changed := rule
	changed.Timezone = "UTC"
	if err := runtime.Reconcile(map[uint][]Rule{1: {changed}}); err == nil {
		t.Fatal("accepted timezone change without revision")
	}
	if err := runtime.Reconcile(map[uint][]Rule{1: {rule, calendarRule(2, "UTC")}}); err == nil {
		t.Fatal("accepted duplicate calendar-day rule")
	}
}
