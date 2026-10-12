package models

// NexusTokenSession holds website authorization separately from inference keys.
// Passwords are never persisted. All keys of one website account share token rotation.
type NexusTokenSession struct {
	ID               string `gorm:"type:varchar(36);primaryKey"`
	BaseURL          string `gorm:"type:varchar(512);not null;uniqueIndex:idx_nexustoken_account,priority:1"`
	UserID           int64  `gorm:"not null;uniqueIndex:idx_nexustoken_account,priority:2"`
	Email            string `gorm:"type:varchar(255);not null"`
	EncryptedPayload string `gorm:"type:text;not null"`
	UpdatedAtMS      int64  `gorm:"not null"`
}
