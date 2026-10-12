// Package nexustoken adapts the website's authenticated carpool API. Website
// tokens are never used for inference, and inference keys never authorize it.
package nexustoken

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gpt-load/internal/channel/spec"
	observation "gpt-load/internal/subscription/providers/observation"
)

type Error struct {
	Status int
	Code   string
}

func (e *Error) Error() string       { return "NexusToken: " + e.Code }
func (e *Error) HTTPStatusCode() int { return e.Status }

type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	ExpiresAtMS  int64  `json:"expires_at_ms"`
	TempToken    string `json:"temp_token,omitempty"`
	User         struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
}

type Asset struct {
	RoundID        int64           `json:"round_id"`
	ActiveSeatIDs  []int64         `json:"active_seat_ids"`
	ProductName    string          `json:"product_name"`
	AssignmentName string          `json:"assignment_display_name"`
	Status         string          `json:"status"`
	EndsAt         string          `json:"ends_at"`
	Usage          json.RawMessage `json:"usage"`
	OpenAIQuota    struct {
		Credits      json.RawMessage `json:"credits_snapshot"`
		ResetCredits json.RawMessage `json:"reset_credit_snapshot"`
	} `json:"openai_quota"`
}

type Binding struct {
	APIKey struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"api_key"`
	SeatID *int64 `json:"seat_id"`
	Seat   *struct {
		RoundID int64 `json:"round_id"`
	} `json:"seat"`
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// WebsiteURL removes the inference /v1 suffix but preserves a deployment prefix.
func WebsiteURL(raw string) (string, error) {
	normalized, err := spec.NormalizeBaseURL(raw)
	if err != nil {
		return "", &Error{Code: "invalid_url"}
	}
	u, err := url.Parse(normalized)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", &Error{Code: "invalid_url"}
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1")
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (c Client) Request(ctx context.Context, method, path, token string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return &Error{Code: "invalid_request"}
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+"/api/v1"+path, body)
	if err != nil {
		return &Error{Code: "invalid_request"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-UI-Request", "1")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if c.HTTP != nil {
		*client = *c.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return &Error{Code: "request_failed"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &Error{Status: response.StatusCode, Code: "upstream_rejected"}
	}
	var envelope struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, (4<<20)+1)).Decode(&envelope); err != nil || envelope.Code == nil || *envelope.Code != 0 || len(envelope.Data) == 0 {
		return &Error{Code: "invalid_response"}
	}
	if output != nil && json.Unmarshal(envelope.Data, output) != nil {
		return &Error{Code: "invalid_response"}
	}
	return nil
}

func (c Client) Login(ctx context.Context, email, password, temporaryToken, totp string) (Tokens, error) {
	var tokens Tokens
	path := "/auth/login"
	input := map[string]string{"email": email, "password": password}
	if temporaryToken != "" {
		path = "/auth/login/2fa"
		input = map[string]string{"temp_token": temporaryToken, "totp_code": totp}
	}
	err := c.Request(ctx, http.MethodPost, path, "", input, &tokens)
	if err == nil && tokens.AccessToken == "" && tokens.TempToken == "" {
		err = &Error{Code: "invalid_response"}
	}
	return tokens, err
}

func (c Client) Assets(ctx context.Context, token string) ([]Asset, error) {
	var assets []Asset
	err := c.Request(ctx, http.MethodGet, "/carpool/my/assets", token, nil, &assets)
	return assets, err
}

// Snapshot retains upstream percentages and costs in distinct personal and
// shared windows. Costs are independent of the upstream percentage limit.
func Snapshot(asset Asset, usageJSON, quotaJSON []byte, email string, now time.Time) (observation.Snapshot, error) {
	result := observation.Snapshot{Plan: observation.PlanSummary{Name: asset.ProductName},
		Account:      &observation.AccountSummary{Email: email, DisplayName: asset.AssignmentName},
		QuotaWindows: []observation.QuotaWindow{}}
	var usage map[string]json.RawMessage
	if len(usageJSON) == 0 {
		usageJSON = asset.Usage
	}
	if len(usageJSON) > 0 && string(usageJSON) != "null" && json.Unmarshal(usageJSON, &usage) != nil {
		return result, &Error{Code: "invalid_response"}
	}
	for _, period := range []struct {
		name    string
		seconds int64
	}{{"5h", 18000}, {"7d", 604800}} {
		for _, subject := range []struct{ key, label, scope string }{{"seat", "Personal", "personal"}, {"account", "Vehicle", "vehicle"}} {
			var percent float64
			raw := usage[subject.key+"_percent_"+period.name]
			if len(raw) == 0 || string(raw) == "null" {
				continue
			}
			if json.Unmarshal(raw, &percent) != nil || percent < 0 {
				return result, &Error{Code: "invalid_response"}
			}
			limit, used, utilization := 100.0, percent, min(percent/100, 1)
			remaining := max(0, 100-percent)
			state := "available"
			if percent >= 100 {
				state = "exhausted"
			}
			window := observation.QuotaWindow{ID: subject.scope + "-" + period.name, Label: subject.label + " " + period.name,
				Scope: subject.scope, Unit: "percent", Used: &used, Limit: &limit, Remaining: &remaining,
				Utilization: &utilization, WindowSeconds: &period.seconds, State: state,
				IsPrimary: period.name == "7d" && subject.scope == "personal"}
			if costJSON := usage[subject.key+"_cost_"+period.name]; len(costJSON) > 0 && string(costJSON) != "null" {
				var cost float64
				if json.Unmarshal(costJSON, &cost) != nil || cost < 0 {
					return result, &Error{Code: "invalid_response"}
				}
				window.UsedCostUSD = &cost
			}
			var reset string
			_ = json.Unmarshal(usage["reset_"+period.name+"_at"], &reset)
			if timestamp, err := time.Parse(time.RFC3339Nano, reset); err == nil {
				value := timestamp.UnixMilli()
				window.ResetAtMS = &value
			}
			result.QuotaWindows = append(result.QuotaWindows, window)
		}
	}
	var quota struct {
		Credits *observation.CreditSummary `json:"credits"`
		Reset   *struct {
			Available *int64 `json:"available_count"`
			Credits   []struct {
				ExpiresAt string `json:"expires_at"`
			} `json:"credits"`
		} `json:"rate_limit_reset_credits"`
	}
	if len(quotaJSON) > 0 && json.Unmarshal(quotaJSON, &quota) != nil {
		return result, &Error{Code: "invalid_response"}
	}
	if quota.Credits == nil && len(asset.OpenAIQuota.Credits) > 0 {
		var cached struct {
			Credits *observation.CreditSummary `json:"credits"`
		}
		if json.Unmarshal(asset.OpenAIQuota.Credits, &cached) == nil {
			quota.Credits = cached.Credits
		}
	}
	if quota.Reset == nil && len(asset.OpenAIQuota.ResetCredits) > 0 {
		_ = json.Unmarshal(asset.OpenAIQuota.ResetCredits, &quota.Reset)
	}
	result.Credits = quota.Credits
	if quota.Reset != nil {
		if quota.Reset.Available != nil && *quota.Reset.Available < 0 {
			return result, &Error{Code: "invalid_response"}
		}
		result.ResetCreditsAvailable = quota.Reset.Available
		for _, credit := range quota.Reset.Credits {
			if timestamp, err := time.Parse(time.RFC3339Nano, credit.ExpiresAt); err == nil && timestamp.After(now) {
				value := timestamp.UnixMilli()
				result.ResetCredits = append(result.ResetCredits, observation.ResetCredit{ExpiresAtMS: &value})
			}
		}
	}
	return result, nil
}

func SeatPath(seatID int64, suffix string) string {
	return fmt.Sprintf("/carpool/my/seats/%s/%s", strconv.FormatInt(seatID, 10), suffix)
}
