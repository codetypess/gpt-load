package accessquota

import (
	"fmt"
	"time"
	_ "time/tzdata"
)

type PeriodMode string

const (
	// An omitted mode preserves the original request-triggered duration window.
	PeriodModeDuration    PeriodMode = ""
	PeriodModeCalendarDay PeriodMode = "calendar_day"
	DefaultTimezone                  = "Asia/Shanghai"
	CalendarDaySeconds    int64      = 86400
)

func ValidateTimezone(name string) error {
	if name == "" || name == "Local" || len(name) > 64 {
		return fmt.Errorf("quota timezone must be an explicit IANA timezone")
	}
	_, err := time.LoadLocation(name)
	return err
}

// ValidatePeriod checks both duration windows and calendar-day definitions.
func (rule Rule) ValidatePeriod() error {
	if rule.Kind == KindTotal {
		if rule.PeriodSeconds != 0 || rule.PeriodMode != PeriodModeDuration || rule.Timezone != "" {
			return fmt.Errorf("total quota must not have a period or timezone")
		}
		return nil
	}
	if rule.Kind != KindPeriodic {
		return fmt.Errorf("invalid quota kind %q", rule.Kind)
	}
	switch rule.PeriodMode {
	case PeriodModeDuration:
		if rule.PeriodSeconds < MinPeriodSeconds || rule.PeriodSeconds > MaxPeriodSeconds || rule.Timezone != "" {
			return fmt.Errorf("invalid quota duration or timezone")
		}
	case PeriodModeCalendarDay:
		if rule.PeriodSeconds != CalendarDaySeconds {
			return fmt.Errorf("calendar-day quota must have a period of one day")
		}
		return ValidateTimezone(rule.Timezone)
	default:
		return fmt.Errorf("invalid quota period mode %q", rule.PeriodMode)
	}
	return nil
}

func (rule *runtimeRule) window(now time.Time) (int64, int64) {
	if rule.definition.PeriodMode == PeriodModeCalendarDay {
		local := now.In(rule.location)
		start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, rule.location)
		return start.UnixMilli(), start.AddDate(0, 0, 1).UnixMilli()
	}
	return now.UnixMilli(), now.Add(time.Duration(rule.definition.PeriodSeconds) * time.Second).UnixMilli()
}
