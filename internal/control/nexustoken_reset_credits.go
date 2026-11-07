package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"gpt-load/internal/nexustoken"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

// NexusToken's public seat reset call has no upstream idempotency contract.
// Reserve before dispatch and never resend a prepared or uncertain reset.
func (s *Service) consumeNexusTokenResetCredit(
	ctx context.Context,
	group models.Group,
	credential models.Credential,
	idempotencyKey string,
) (ResetCreditConsumeResponse, error) {
	canonical, _, err := s.decodeCredential(group, credential)
	if err != nil {
		return ResetCreditConsumeResponse{}, err
	}
	defer clear(canonical)
	var fields, params map[string]string
	if json.Unmarshal(canonical, &fields) != nil || json.Unmarshal(group.Params, &params) != nil {
		return ResetCreditConsumeResponse{}, app_errors.ErrValidation
	}
	baseURL, err := nexustoken.WebsiteURL(params["base_url"])
	seatID, seatErr := strconv.ParseInt(fields["seat_id"], 10, 64)
	keyID, keyErr := strconv.ParseInt(fields["key_id"], 10, 64)
	if err != nil || seatErr != nil || keyErr != nil || seatID <= 0 || keyID <= 0 || fields["session_id"] == "" {
		return ResetCreditConsumeResponse{}, app_errors.ErrCredentialReauthorizationRequired
	}
	// The full fingerprint includes the website session and seat binding; the
	// inference identity alone would allow replay after relinking the same key.
	digest := resetCreditRequestDigest(group.ID, credential.ID, credential.Fingerprint, baseURL)
	operation, replayed, err := s.beginResetCreditOperationWithRetry(ctx, group.ID, credential.ID, digest, idempotencyKey, false)
	if err != nil {
		return ResetCreditConsumeResponse{}, err
	}
	if replayed {
		return s.replayResetCreditOperation(ctx, group.ID, credential.ID, operation)
	}
	rejectPreparation := func(cause error) (ResetCreditConsumeResponse, error) {
		apiErr := classifyResetCreditPreparationError(nexusTokenResetError(cause))
		if s.finishResetCreditOperation(operation, models.CredentialResetOperationRejected, nil, apiErr.Code) != nil {
			return ResetCreditConsumeResponse{}, app_errors.ErrResetCreditOutcomeUnknown
		}
		return ResetCreditConsumeResponse{}, apiErr
	}
	network, err := s.credentialNetworkContext(ctx, s.db, group, credential)
	if err != nil {
		return rejectPreparation(err)
	}
	callContext, cancel := context.WithTimeout(subscriptionruntime.WithNetworkContext(ctx, network), defaultSubscriptionControlTimeout)
	defer cancel()
	if err := s.validateNexusTokenResetBinding(callContext, fields["session_id"], baseURL, seatID, keyID); err != nil {
		return rejectPreparation(err)
	}
	// Refuse a changed local binding after the upstream ownership checks.
	currentGroup, currentCredential, _, err := s.loadObservationTarget(callContext, group.ID, credential.ID)
	if err != nil || !sameSubscriptionTarget(group, currentGroup) || currentCredential.Fingerprint != credential.Fingerprint {
		return rejectPreparation(app_errors.ErrValidation)
	}
	var result struct {
		WindowsReset *int `json:"windows_reset"`
	}
	err = s.nexusTokenRequest(callContext, fields["session_id"], baseURL, http.MethodPost, nexustoken.SeatPath(seatID, "openai-quota/reset"), &result)
	if err == nil && (result.WindowsReset == nil || *result.WindowsReset < 0) {
		err = errors.New("invalid NexusToken reset result")
	}
	if err != nil {
		state, apiErr := classifyResetCreditConsumeError(nexusTokenResetError(err))
		if s.finishResetCreditOperation(operation, state, nil, apiErr.Code) != nil {
			return ResetCreditConsumeResponse{}, app_errors.ErrResetCreditOutcomeUnknown
		}
		return ResetCreditConsumeResponse{}, apiErr
	}
	return s.completeCredentialResetCredit(ctx, group, credential, operation, storedResetCreditResult{
		Status: "succeeded", WindowsReset: *result.WindowsReset,
	})
}

func (s *Service) validateNexusTokenResetBinding(ctx context.Context, sessionID, baseURL string, seatID, keyID int64) error {
	var assets []nexustoken.Asset
	if err := s.nexusTokenRequest(ctx, sessionID, baseURL, http.MethodGet, "/carpool/my/assets", &assets); err != nil {
		return err
	}
	var roundID int64
	for _, asset := range assets {
		for _, id := range asset.ActiveSeatIDs {
			if id == seatID {
				roundID = asset.RoundID
				break
			}
		}
	}
	if roundID <= 0 {
		return &subscriptionruntime.UpstreamHTTPError{StatusCode: http.StatusForbidden}
	}
	var bindings []nexustoken.Binding
	if err := s.nexusTokenRequest(ctx, sessionID, baseURL, http.MethodGet, "/carpool/my/api-key-bindings", &bindings); err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding.APIKey.ID == keyID && ((binding.SeatID != nil && *binding.SeatID == seatID) ||
			(binding.SeatID == nil && binding.Seat != nil && binding.Seat.RoundID == roundID)) {
			return nil
		}
	}
	return &subscriptionruntime.UpstreamHTTPError{StatusCode: http.StatusForbidden}
}

func nexusTokenResetError(err error) error {
	var upstream *nexustoken.Error
	if errors.As(err, &upstream) && upstream.Status >= 400 && upstream.Status < 500 {
		return &subscriptionruntime.UpstreamHTTPError{StatusCode: upstream.Status}
	}
	return err
}
