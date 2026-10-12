package control

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/sirupsen/logrus"

	"gpt-load/internal/channel"
	"gpt-load/internal/storage/models"
)

// SyncNexusTokenObservations shares the manual refresh path, including session
// rotation, request limits, single-flight coordination and cached quota recovery.
func (s *Service) SyncNexusTokenObservations(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	var groups []models.Group
	err := s.db.WithContext(ctx).
		Where("channel_id = ? AND enabled = ?", string(channel.NexusToken), true).
		Preload("Credentials", "status = ? AND auth_state = ?", models.CredentialStatusActive, models.CredentialAuthStateReady).
		Order("id ASC").Find(&groups).Error
	if err != nil {
		if ctx.Err() == nil {
			logrus.WithError(err).WithField("event", "control.nexustoken_sync_targets_failed").Warn("load NexusToken sync targets failed")
		}
		return
	}
	for _, group := range groups {
		for _, credential := range group.Credentials {
			if ctx.Err() != nil {
				return
			}
			_, err := s.refreshCredentialObservationForTarget(ctx, group.ID, credential.ID, observationRefreshAutomatic, &group)
			if err != nil && ctx.Err() == nil {
				logrus.WithFields(logrus.Fields{
					"event": "control.nexustoken_sync_failed", "group_id": group.ID, "credential_id": credential.ID,
				}).Warn("NexusToken automatic sync failed; retrying next interval")
			}
		}
	}
}

func (s *Service) nexusTokenObservationBound(group models.Group, credential models.Credential) bool {
	canonical, _, err := s.decodeCredential(group, credential)
	if err != nil {
		return false
	}
	defer clear(canonical)
	var fields map[string]string
	if json.Unmarshal(canonical, &fields) != nil {
		return false
	}
	seatID, _ := strconv.ParseInt(fields["seat_id"], 10, 64)
	keyID, _ := strconv.ParseInt(fields["key_id"], 10, 64)
	return fields["session_id"] != "" && seatID > 0 && keyID > 0
}
