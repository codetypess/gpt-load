package control

import (
	"bytes"
	"context"
	"encoding/json"

	"gpt-load/internal/channel"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

func sameSubscriptionTarget(before, after models.Group) bool {
	return before.ID == after.ID && before.ChannelID == after.ChannelID &&
		before.ConnectionType == after.ConnectionType && bytes.Equal(bytes.TrimSpace(before.Params), bytes.TrimSpace(after.Params))
}

func (s *Service) restoreCredentialRuntimeForTarget(ctx context.Context, group models.Group, credential models.Credential) (bool, bool, error) {
	// 上游已确认消耗成功，客户端取消不能中断本地收尾。
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlTransactionCleanupTimeout)
	defer cancel()
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	current, err := loadGroupRow(s.db.WithContext(finalizeContext), group.ID)
	if err != nil {
		return false, false, err
	}
	if !sameSubscriptionTarget(group, current) {
		return false, false, nil
	}
	if group.ChannelID == string(channel.NexusToken) {
		var currentCredential models.Credential
		if err := s.db.WithContext(finalizeContext).Select("fingerprint").Take(&currentCredential, "id = ? AND group_id = ?", credential.ID, group.ID).Error; err != nil {
			return false, false, err
		}
		if currentCredential.Fingerprint != credential.Fingerprint {
			return false, false, nil
		}
	}
	return s.restoreCredentialRuntimeAfterReset(credential.ID), true, nil
}

// resolveSubscriptionTarget validates stored channel parameters and freezes
// their resolved provider configuration for one subscription operation.
func (s *Service) resolveSubscriptionTarget(
	channelID channel.ID,
	params []byte,
) (subscriptionruntime.Target, error) {
	resolved, err := s.channelRegistry.Resolve(channelID, json.RawMessage(params))
	if err != nil {
		return subscriptionruntime.Target{}, err
	}
	return subscriptionruntime.NewTarget(resolved.TargetConfig), nil
}

// subscriptionTargetFromResolved copies a previously validated channel target
// into the provider-neutral subscription runtime representation.
func subscriptionTargetFromResolved(resolved channel.ResolvedTarget) subscriptionruntime.Target {
	return subscriptionruntime.NewTarget(resolved.TargetConfig)
}
