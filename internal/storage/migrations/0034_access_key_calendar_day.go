package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

const ID0034 = "0034_access_key_calendar_day"

// Keep the original rule/state tables and checkpoints intact. Duration rules
// retain their empty mode; calendar days have a distinct identity from 24 hours.
type accessKeyCostLimitRule0034 struct {
	AccessKeyID   uint   `gorm:"uniqueIndex:idx_access_key_cost_limit_rules_identity,priority:1"`
	Kind          string `gorm:"uniqueIndex:idx_access_key_cost_limit_rules_identity,priority:2"`
	PeriodSeconds int64  `gorm:"uniqueIndex:idx_access_key_cost_limit_rules_identity,priority:3"`
	PeriodMode    string `gorm:"type:varchar(16);not null;default:'';uniqueIndex:idx_access_key_cost_limit_rules_identity,priority:4"`
	Timezone      string `gorm:"type:varchar(64);not null;default:'';check:chk_ak_cost_rule_period_mode,(period_mode = '' AND timezone = '') OR (kind = 'periodic' AND period_mode = 'calendar_day' AND period_seconds = 86400 AND timezone <> '')"`
}

func (accessKeyCostLimitRule0034) TableName() string { return "access_key_cost_limit_rules" }

func Up0034(db *gorm.DB) error {
	if err := ValidateRecoverable0034(db); err != nil {
		return err
	}
	model := &accessKeyCostLimitRule0034{}
	for _, field := range []string{"PeriodMode", "Timezone"} {
		if !db.Migrator().HasColumn(model, field) {
			if field == "Timezone" && db.Dialector.Name() == "sqlite" {
				// SQLite accepts the named check with ADD COLUMN, avoiding a
				// table rebuild inside the migration runner's pinned transaction.
				if err := db.Exec(`ALTER TABLE access_key_cost_limit_rules ADD COLUMN timezone varchar(64) NOT NULL DEFAULT ''
					CONSTRAINT chk_ak_cost_rule_period_mode CHECK ((period_mode = '' AND timezone = '') OR
					(kind = 'periodic' AND period_mode = 'calendar_day' AND period_seconds = 86400 AND timezone <> ''))`).Error; err != nil {
					return err
				}
				continue
			}
			if err := db.Migrator().AddColumn(model, field); err != nil {
				return fmt.Errorf("add calendar-day quota %s: %w", field, err)
			}
		}
	}
	const index = "idx_access_key_cost_limit_rules_identity"
	if db.Migrator().HasIndex(model, index) {
		if err := db.Migrator().DropIndex(model, index); err != nil {
			return err
		}
	}
	if err := db.Migrator().CreateIndex(model, index); err != nil {
		return err
	}
	if !db.Migrator().HasConstraint(model, "chk_ak_cost_rule_period_mode") {
		if db.Dialector.Name() == "sqlite" {
			return fmt.Errorf("calendar-day quota constraint is missing from existing timezone column")
		}
		if err := db.Migrator().CreateConstraint(model, "chk_ak_cost_rule_period_mode"); err != nil {
			return err
		}
	}
	return Validate0034(db)
}

func ValidateRecoverable0034(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "sqlite", "mysql", "postgres":
	default:
		return fmt.Errorf("unsupported calendar-day quota migration driver %q", db.Dialector.Name())
	}
	if !db.Migrator().HasTable(&accessKeyCostLimitRule0034{}) {
		return fmt.Errorf("access key cost limit rules table is missing")
	}
	return nil
}

func Validate0034(db *gorm.DB) error {
	model := &accessKeyCostLimitRule0034{}
	for _, field := range []string{"PeriodMode", "Timezone"} {
		if !db.Migrator().HasColumn(model, field) {
			return fmt.Errorf("calendar-day quota column %s is missing", field)
		}
	}
	if !db.Migrator().HasIndex(model, "idx_access_key_cost_limit_rules_identity") ||
		!db.Migrator().HasConstraint(model, "chk_ak_cost_rule_period_mode") {
		return fmt.Errorf("calendar-day quota identity or constraint is missing")
	}
	return nil
}
