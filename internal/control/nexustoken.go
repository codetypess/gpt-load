package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gpt-load/internal/channel"
	"gpt-load/internal/nexustoken"
	"gpt-load/internal/outboundproxy"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"
	"gpt-load/internal/storage/models"
	subscriptionruntime "gpt-load/internal/subscription/runtime"
)

type nexusTokenLoginRequest struct {
	BaseURL   string                `json:"base_url"`
	Email     string                `json:"email"`
	Password  string                `json:"password"`
	TempToken string                `json:"temp_token,omitempty"`
	TOTPCode  string                `json:"totp_code,omitempty"`
	Proxy     *outboundproxy.Config `json:"proxy,omitempty"`
	GroupID   uint                  `json:"group_id,omitempty"`
}

type nexusTokenImportChoice struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Plan       string `json:"plan"`
	Credential string `json:"credential"`
}

type nexusTokenLoginResult struct {
	TempToken string                   `json:"temp_token,omitempty"`
	Email     string                   `json:"email,omitempty"`
	Choices   []nexusTokenImportChoice `json:"choices"`
}

func (server *Server) handleNexusTokenLogin(c *gin.Context) {
	var request nexusTokenLoginRequest
	if err := bindStrictJSON(c, &request); err != nil {
		writeServiceError(c, "nexustoken_login", mapControlJSONError(err))
		return
	}
	defer func() { request.Password = ""; request.TempToken = ""; request.TOTPCode = "" }()
	result, err := server.service.loginNexusToken(c.Request.Context(), request)
	if err != nil {
		writeServiceError(c, "nexustoken_login", err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.SuccessI18n(c, "common.success", result)
}

func nexusTokenAPIError(code string, status int) error {
	return &app_errors.APIError{HTTPStatus: status, Code: "NEXUSTOKEN_" + code, Message: "NexusToken account connection failed"}
}

func (s *Service) loginNexusToken(ctx context.Context, request nexusTokenLoginRequest) (nexusTokenLoginResult, error) {
	result := nexusTokenLoginResult{Choices: []nexusTokenImportChoice{}}
	baseURL, err := nexustoken.WebsiteURL(request.BaseURL)
	if err != nil || len(request.Email) > 255 || len(request.Password) > 4096 || (request.TempToken == "" && (strings.TrimSpace(request.Email) == "" || request.Password == "")) {
		return result, app_errors.ErrValidation
	}
	network, err := s.draftNetworkContext(ctx, request.Proxy)
	if request.GroupID != 0 {
		if request.Proxy != nil {
			return result, app_errors.ErrValidation
		}
		var group models.Group
		if err := s.db.WithContext(ctx).Take(&group, request.GroupID).Error; err != nil {
			return result, app_errors.ParseDBError(err)
		}
		if group.ChannelID != string(channel.NexusToken) {
			return result, app_errors.ErrValidation
		}
		var params map[string]string
		_ = json.Unmarshal(group.Params, &params)
		expected, _ := nexustoken.WebsiteURL(params["base_url"])
		if expected != baseURL {
			return result, app_errors.ErrValidation
		}
		network, err = s.groupNetworkContext(ctx, s.db, group)
	}
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(subscriptionruntime.WithNetworkContext(ctx, network), defaultSubscriptionControlTimeout)
	defer cancel()
	httpClient, err := subscriptionruntime.HTTPClient(ctx)
	if err != nil {
		return result, app_errors.ErrBadGateway
	}
	client := nexustoken.Client{BaseURL: baseURL, HTTP: httpClient}
	tokens, err := client.Login(ctx, strings.TrimSpace(request.Email), request.Password, request.TempToken, request.TOTPCode)
	if err != nil {
		return result, nexusTokenAPIError("LOGIN_FAILED", http.StatusBadRequest)
	}
	if tokens.TempToken != "" && tokens.AccessToken == "" {
		result.TempToken = tokens.TempToken
		return result, nil
	}
	if tokens.User.ID <= 0 || tokens.User.Email == "" {
		return result, nexusTokenAPIError("INVALID_RESPONSE", http.StatusBadGateway)
	}
	assets, err := client.Assets(ctx, tokens.AccessToken)
	if err != nil {
		return result, nexusTokenAPIError("ASSETS_FAILED", http.StatusBadGateway)
	}
	var bindings []nexustoken.Binding
	if err := client.Request(ctx, http.MethodGet, "/carpool/my/api-key-bindings", tokens.AccessToken, nil, &bindings); err != nil {
		return result, nexusTokenAPIError("BINDINGS_FAILED", http.StatusBadGateway)
	}
	s.nexusTokenMu.Lock()
	defer s.nexusTokenMu.Unlock()
	var session models.NexusTokenSession
	err = s.db.WithContext(ctx).Where("base_url = ? AND user_id = ?", baseURL, tokens.User.ID).Take(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		id, idErr := newOperationID(s.random)
		if idErr != nil {
			return result, app_errors.ErrInternalServer
		}
		session = models.NexusTokenSession{ID: id, BaseURL: baseURL, UserID: tokens.User.ID}
	} else if err != nil {
		return result, app_errors.ParseDBError(err)
	}
	session.Email = tokens.User.Email
	if err := s.saveNexusTokenSession(ctx, &session, tokens); err != nil {
		return result, err
	}
	result.Email = session.Email
	for index, binding := range bindings {
		if index >= maxCredentialLines {
			return result, app_errors.ErrValidation
		}
		if binding.APIKey.ID <= 0 {
			continue
		}
		var matched *nexustoken.Asset
		var seatID int64
		for i := range assets {
			asset := &assets[i]
			if asset.Status != "active" || len(asset.ActiveSeatIDs) == 0 {
				continue
			}
			if binding.SeatID != nil {
				for _, id := range asset.ActiveSeatIDs {
					if id == *binding.SeatID {
						matched = asset
						seatID = id
						break
					}
				}
			} else if binding.Seat != nil && binding.Seat.RoundID == asset.RoundID {
				matched = asset
				seatID = asset.ActiveSeatIDs[0]
			}
			if matched != nil {
				break
			}
		}
		if matched == nil {
			continue
		}
		var key struct {
			ID    int64  `json:"id"`
			Scope string `json:"scope"`
			Key   string `json:"key"`
		}
		if err := client.Request(ctx, http.MethodGet, fmt.Sprintf("/keys/%d", binding.APIKey.ID), tokens.AccessToken, nil, &key); err != nil {
			return result, nexusTokenAPIError("KEYS_FAILED", http.StatusBadGateway)
		}
		if key.ID != binding.APIKey.ID || key.Scope != "carpool" || strings.TrimSpace(key.Key) == "" {
			continue
		}
		fields := map[string]string{"api_key": key.Key, "session_id": session.ID, "seat_id": strconv.FormatInt(seatID, 10), "key_id": strconv.FormatInt(key.ID, 10), "email": session.Email}
		if plan := strings.TrimSpace(matched.ProductName); plan != "" {
			fields["plan_name"] = plan
		}
		credential, _ := json.Marshal(fields)
		result.Choices = append(result.Choices, nexusTokenImportChoice{ID: binding.APIKey.ID, Name: binding.APIKey.Name, Plan: matched.ProductName, Credential: string(credential)})
	}
	return result, nil
}

func (s *Service) saveNexusTokenSession(ctx context.Context, session *models.NexusTokenSession, tokens nexustoken.Tokens) error {
	if tokens.ExpiresIn > 0 {
		tokens.ExpiresAtMS = s.now().Add(time.Duration(tokens.ExpiresIn) * time.Second).UnixMilli()
	}
	encoded, err := json.Marshal(tokens)
	if err != nil {
		return app_errors.ErrInternalServer
	}
	defer clear(encoded)
	session.EncryptedPayload, err = s.encryption.Encrypt(string(encoded))
	if err != nil {
		return app_errors.ErrInternalServer
	}
	session.UpdatedAtMS = s.now().UnixMilli()
	if err := s.db.WithContext(ctx).Save(session).Error; err != nil {
		return app_errors.ParseDBError(err)
	}
	return nil
}

// Rotation is serialized across keys that share a website account. Refreshed
// tokens are persisted before retrying a request, including after a 401.
func (s *Service) nexusTokenRequest(ctx context.Context, sessionID, baseURL, method, path string, output any) error {
	s.nexusTokenMu.Lock()
	defer s.nexusTokenMu.Unlock()
	var session models.NexusTokenSession
	if err := s.db.WithContext(ctx).Where("id = ? AND base_url = ?", sessionID, baseURL).Take(&session).Error; err != nil {
		return &nexustoken.Error{Status: 401, Code: "session_unavailable"}
	}
	plaintext, err := s.encryption.Decrypt(session.EncryptedPayload)
	if err != nil {
		return &nexustoken.Error{Code: "session_invalid"}
	}
	var tokens nexustoken.Tokens
	err = json.Unmarshal([]byte(plaintext), &tokens)
	plaintext = ""
	if err != nil {
		return &nexustoken.Error{Code: "session_invalid"}
	}
	httpClient, err := subscriptionruntime.HTTPClient(ctx)
	if err != nil {
		return err
	}
	client := nexustoken.Client{BaseURL: baseURL, HTTP: httpClient}
	refresh := func() error {
		if tokens.RefreshToken == "" {
			return &nexustoken.Error{Status: 401, Code: "session_expired"}
		}
		var rotated nexustoken.Tokens
		if err := client.Request(ctx, http.MethodPost, "/auth/refresh", "", map[string]string{"refresh_token": tokens.RefreshToken}, &rotated); err != nil {
			return err
		}
		if rotated.AccessToken == "" || (rotated.User.ID != 0 && rotated.User.ID != session.UserID) {
			return &nexustoken.Error{Code: "session_invalid"}
		}
		if rotated.RefreshToken == "" {
			rotated.RefreshToken = tokens.RefreshToken
		}
		rotated.User = tokens.User
		if err := s.saveNexusTokenSession(ctx, &session, rotated); err != nil {
			return err
		}
		tokens = rotated
		return nil
	}
	refreshed := false
	if tokens.ExpiresAtMS > 0 && tokens.ExpiresAtMS <= s.now().Add(time.Minute).UnixMilli() {
		if err := refresh(); err != nil {
			return err
		}
		refreshed = true
	}
	err = client.Request(ctx, method, path, tokens.AccessToken, nil, output)
	var upstream *nexustoken.Error
	if !refreshed && errors.As(err, &upstream) && upstream.Status == 401 {
		if err := refresh(); err != nil {
			return err
		}
		return client.Request(ctx, method, path, tokens.AccessToken, nil, output)
	}
	return err
}

func (s *Service) refreshNexusTokenObservation(ctx context.Context, group models.Group, credential models.Credential, previous models.CredentialObservation, flight *observationFlight) (CredentialObservationResponse, error) {
	attempt := s.now().UnixMilli()
	canonical, _, err := s.decodeCredential(group, credential)
	if err != nil {
		return CredentialObservationResponse{}, err
	}
	defer clear(canonical)
	var fields, params map[string]string
	_ = json.Unmarshal(canonical, &fields)
	_ = json.Unmarshal(group.Params, &params)
	baseURL, err := nexustoken.WebsiteURL(params["base_url"])
	seatID, seatErr := strconv.ParseInt(fields["seat_id"], 10, 64)
	code := "observation_authorization_failed"
	var assets []nexustoken.Asset
	if err == nil && seatErr == nil && seatID > 0 && fields["session_id"] != "" {
		code = "observation_upstream_failed"
		err = s.nexusTokenRequest(ctx, fields["session_id"], baseURL, http.MethodGet, "/carpool/my/assets", &assets)
	} else {
		err = app_errors.ErrValidation
	}
	var asset *nexustoken.Asset
	if err == nil {
		for i := range assets {
			for _, id := range assets[i].ActiveSeatIDs {
				if id == seatID {
					asset = &assets[i]
					break
				}
			}
			if asset != nil {
				break
			}
		}
		if asset == nil {
			err = app_errors.ErrResourceNotFound
			code = "observation_access_denied"
		}
	}
	var usage, quota json.RawMessage
	partial := false
	if err == nil {
		err = s.nexusTokenRequest(ctx, fields["session_id"], baseURL, http.MethodGet, nexustoken.SeatPath(seatID, "usage"), &usage)
	}
	if err == nil {
		var bindings []nexustoken.Binding
		err = s.nexusTokenRequest(ctx, fields["session_id"], baseURL, http.MethodGet, "/carpool/my/api-key-bindings", &bindings)
		if err == nil {
			keyID, _ := strconv.ParseInt(fields["key_id"], 10, 64)
			matched := false
			for _, binding := range bindings {
				if binding.APIKey.ID == keyID && ((binding.SeatID != nil && *binding.SeatID == seatID) ||
					(binding.SeatID == nil && binding.Seat != nil && binding.Seat.RoundID == asset.RoundID)) {
					matched = true
					break
				}
			}
			if !matched {
				err = app_errors.ErrValidation
				code = "observation_access_denied"
			}
		}
	}
	if err == nil {
		// Refresh retrieves both points and reset cards; it does not consume a card.
		quotaErr := s.nexusTokenRequest(ctx, fields["session_id"], baseURL, http.MethodPost, nexustoken.SeatPath(seatID, "openai-quota/refresh"), &quota)
		partial = quotaErr != nil
	}
	var snapshot CredentialObservationSnapshot
	if err == nil {
		normalized, normalizeErr := nexustoken.Snapshot(*asset, usage, quota, fields["email"], s.now())
		err = normalizeErr
		if err == nil {
			body, _ := json.Marshal(normalized)
			err = json.Unmarshal(body, &snapshot)
		}
	}
	if partial && err == nil {
		var cached CredentialObservationSnapshot
		if json.Unmarshal(previous.SnapshotJSON, &cached) == nil {
			if snapshot.Credits == nil {
				snapshot.Credits = cached.Credits
			}
			if snapshot.ResetCreditsAvailable == nil {
				snapshot.ResetCreditsAvailable = cached.ResetCreditsAvailable
				snapshot.ResetCredits = cached.ResetCredits
			}
		}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if !s.observationFlightCurrent(group.ID, credential.ID, flight) {
		return s.GetCredentialObservation(ctx, group.ID, credential.ID)
	}
	// A key can be relinked to a different website account without changing its
	// inference identity. Do not publish a snapshot fetched for the old binding.
	var current models.Credential
	if err := s.db.WithContext(ctx).Select("secret_version", "fingerprint").Take(&current, credential.ID).Error; err != nil {
		return CredentialObservationResponse{}, app_errors.ParseDBError(err)
	}
	if current.SecretVersion != credential.SecretVersion || current.Fingerprint != credential.Fingerprint {
		return s.GetCredentialObservation(ctx, group.ID, credential.ID)
	}
	if err != nil {
		var upstream *nexustoken.Error
		if errors.As(err, &upstream) && upstream.Status == 401 {
			code = "observation_authorization_failed"
		}
		return s.recordCredentialObservationFailure(ctx, credential, previous, attempt, code, "refresh NexusToken information", nil)
	}
	completed := s.now().UnixMilli()
	encoded, _ := json.Marshal(snapshot)
	row := models.CredentialObservation{CredentialID: credential.ID, IdentityFingerprint: credential.IdentityFingerprint, SchemaVersion: 1,
		ObservationVersion: previous.ObservationVersion + 1, SnapshotJSON: models.JSON(encoded), State: models.CredentialObservationFresh,
		ObservedAtMS: &completed, LastAttemptAtMS: &attempt, UpdatedAtMS: completed}
	if partial {
		row.LastErrorCode = "observation_partial"
		row.State = models.CredentialObservationStale
	}
	if len(snapshot.QuotaWindows) == 0 {
		row.State = models.CredentialObservationStale
		row.LastErrorCode = "observation_partial"
	}
	if err := s.upsertCredentialObservation(ctx, row); err != nil {
		return CredentialObservationResponse{}, err
	}
	result := mapCredentialObservation(row)
	return result, nil
}
