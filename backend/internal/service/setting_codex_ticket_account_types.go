package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// Missing settings retain the original all-accounts policy; an explicit empty
// array selects no accounts. Return a new slice so callers cannot mutate defaults.
func DefaultOpenAICodexTicketAccountTypes() []string {
	return []string{"free", "plus", "pro", "prolite", "team", "business", "self_serve_business_prolite", "self_serve_business_usage_based", "enterprise", "edu", "other"}
}

func NormalizeOpenAICodexTicketAccountTypes(values []string) ([]string, error) {
	if values == nil {
		return DefaultOpenAICodexTicketAccountTypes(), nil
	}
	selected := make(map[string]bool, len(values))
	allowed := DefaultOpenAICodexTicketAccountTypes()
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !slices.Contains(allowed, value) {
			return nil, errors.New("unsupported Codex ticket account type")
		}
		selected[value] = true
	}
	result := make([]string, 0, len(selected))
	for _, value := range allowed {
		if selected[value] {
			result = append(result, value)
		}
	}
	return result, nil
}

func parseOpenAICodexTicketAccountTypes(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return DefaultOpenAICodexTicketAccountTypes(), nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
		return nil, errors.New("Codex ticket account types must be an array")
	}
	return NormalizeOpenAICodexTicketAccountTypes(values)
}

// Subscription plans, not credential formats (oauth/setup-token), define scope.
// Unknown and missing plans are explicitly controlled by the "other" option.
func OpenAICodexTicketAccountTypeSelected(account *Account, selected []string) bool {
	if !isOpenAICodexTicketAccount(account) {
		return false
	}
	if selected == nil {
		return true
	}
	plan := strings.ToLower(strings.TrimSpace(account.GetCredential("plan_type")))
	switch plan {
	case "pro_lite", "pro-lite":
		plan = "prolite"
	case "education":
		plan = "edu"
	}
	if !slices.Contains(DefaultOpenAICodexTicketAccountTypes(), plan) {
		plan = "other"
	}
	return slices.Contains(selected, plan)
}

func (s *OpenAIGatewayService) openAICodexTicketAccountTypes(ctx context.Context) []string {
	if s == nil || s.settingService == nil {
		return nil // Legacy all-accounts policy.
	}
	return s.settingService.GetOpenAICodexTicketAccountTypes(ctx)
}

func (s *OpenAIGatewayService) openAICodexTicketAccountSelected(ctx context.Context, account *Account) bool {
	return OpenAICodexTicketAccountTypeSelected(account, s.openAICodexTicketAccountTypes(ctx))
}

type cachedOpenAICodexTicketAccountTypes struct {
	value     []string
	expiresAt int64
}

// GetOpenAICodexTicketAccountTypes refreshes every five seconds across instances.
// During transient storage errors retain the last known policy, including [].
func (s *SettingService) GetOpenAICodexTicketAccountTypes(ctx context.Context) []string {
	if ctx == nil {
		ctx = context.Background()
	}
	fallback := DefaultOpenAICodexTicketAccountTypes()
	if s == nil || s.settingRepo == nil {
		return fallback
	}
	if cached, ok := s.openAICodexTicketAccountTypesCache.Load().(*cachedOpenAICodexTicketAccountTypes); ok && cached != nil {
		fallback = slices.Clone(cached.value)
		if time.Now().UnixNano() < cached.expiresAt {
			return fallback
		}
	}
	if ctx.Err() != nil {
		return fallback
	}
	resultCh := s.openAICodexTicketAccountTypesSF.DoChan(SettingKeyOpenAICodexTicketAccountTypes, func() (any, error) {
		lastKnown := DefaultOpenAICodexTicketAccountTypes()
		if cached, ok := s.openAICodexTicketAccountTypesCache.Load().(*cachedOpenAICodexTicketAccountTypes); ok && cached != nil {
			lastKnown = slices.Clone(cached.value)
			if time.Now().UnixNano() < cached.expiresAt {
				return lastKnown, nil
			}
		}
		// A cancelled caller must not cancel a shared read for other requests.
		dbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAICodexTicketAccountTypes)
		if errors.Is(err, ErrSettingNotFound) {
			raw, err = "", nil
		}
		values, parseErr := parseOpenAICodexTicketAccountTypes(raw)
		ttl := 5 * time.Second
		if err != nil || parseErr != nil {
			values, ttl = lastKnown, time.Second
		}
		s.openAICodexTicketAccountTypesCache.Store(&cachedOpenAICodexTicketAccountTypes{
			value: slices.Clone(values), expiresAt: time.Now().Add(ttl).UnixNano(),
		})
		return values, nil
	})
	select {
	case <-ctx.Done():
		return fallback
	case result := <-resultCh:
		if values, ok := result.Val.([]string); ok && result.Err == nil {
			return slices.Clone(values)
		}
		return fallback
	}
}

func (s *SettingService) InvalidateOpenAICodexTicketAccountTypesCache() {
	if s == nil {
		return
	}
	s.openAICodexTicketAccountTypesSF.Forget(SettingKeyOpenAICodexTicketAccountTypes)
	if cached, ok := s.openAICodexTicketAccountTypesCache.Load().(*cachedOpenAICodexTicketAccountTypes); ok && cached != nil {
		s.openAICodexTicketAccountTypesCache.Store(&cachedOpenAICodexTicketAccountTypes{value: slices.Clone(cached.value)})
	}
}
