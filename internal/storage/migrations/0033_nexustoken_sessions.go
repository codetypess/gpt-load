package migrations

import (
	"fmt"
	"gorm.io/gorm"
	"gpt-load/internal/storage/models"
)

const ID0033 = "0033_nexustoken_sessions"

func Up0033(db *gorm.DB) error {
	if err := ValidateRecoverable0033(db); err != nil {
		return err
	}
	if err := db.AutoMigrate(&models.NexusTokenSession{}); err != nil {
		return err
	}
	return Validate0033(db)
}

func ValidateRecoverable0033(db *gorm.DB) error {
	switch db.Dialector.Name() {
	case "sqlite", "mysql", "postgres":
		return nil
	default:
		return fmt.Errorf("unsupported NexusToken session database")
	}
}

func Validate0033(db *gorm.DB) error {
	model := &models.NexusTokenSession{}
	if !db.Migrator().HasTable(model) {
		return fmt.Errorf("NexusToken sessions table is missing")
	}
	for _, field := range []string{"ID", "BaseURL", "UserID", "Email", "EncryptedPayload", "UpdatedAtMS"} {
		if !db.Migrator().HasColumn(model, field) {
			return fmt.Errorf("NexusToken session column %s is missing", field)
		}
	}
	if !db.Migrator().HasIndex(model, "idx_nexustoken_account") {
		return fmt.Errorf("NexusToken account index is missing")
	}
	return nil
}
