package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketAccountTypeSelection(t *testing.T) {
	for _, tc := range []struct {
		plan     string
		selected []string
		want     bool
	}{
		{"team", nil, true}, {"team", []string{}, false},
		{"team", []string{"plus"}, false}, {" Team ", []string{"team"}, true},
		{"self_serve_business_prolite", []string{"self_serve_business_prolite"}, true},
		{"self_serve_business_usage_based", []string{"self_serve_business_prolite"}, false},
		{"self_serve_business_usage_based", []string{"self_serve_business_usage_based"}, true},
		{"pro_lite", []string{"prolite"}, true}, {"education", []string{"edu"}, true},
		{"", []string{"other"}, true}, {"future-plan", []string{"other"}, true},
		{"", []string{"team"}, false},
	} {
		a := ticketTestAccount(41)
		a.Credentials["plan_type"] = tc.plan
		require.Equal(t, tc.want, OpenAICodexTicketAccountTypeSelected(a, tc.selected), "plan=%q selected=%v", tc.plan, tc.selected)
	}
	require.False(t, OpenAICodexTicketAccountTypeSelected(nil, nil))
	a := ticketTestAccount(41)
	a.Type = AccountTypeAPIKey
	require.False(t, OpenAICodexTicketAccountTypeSelected(a, nil))
	values, err := NormalizeOpenAICodexTicketAccountTypes([]string{" TEAM ", "plus", "team"})
	require.NoError(t, err)
	require.Equal(t, []string{"plus", "team"}, values)
	_, err = NormalizeOpenAICodexTicketAccountTypes([]string{"typo"})
	require.Error(t, err)
}

func TestCodexTicketAccountTypeRuntimeCache(t *testing.T) {
	key := SettingKeyOpenAICodexTicketAccountTypes
	repo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{}}}
	s := NewSettingService(repo, &config.Config{})
	ctx := context.Background()
	require.Equal(t, DefaultOpenAICodexTicketAccountTypes(), s.GetOpenAICodexTicketAccountTypes(ctx))
	repo.values[key] = `["team"]`
	s.InvalidateOpenAICodexTicketAccountTypesCache()
	values := s.GetOpenAICodexTicketAccountTypes(ctx)
	require.Equal(t, []string{"team"}, values)
	values[0] = "plus"
	require.Equal(t, []string{"team"}, s.GetOpenAICodexTicketAccountTypes(ctx))
	// Simulate another instance changing scope, then the five-second cache expiry.
	repo.values[key] = `[]`
	s.openAICodexTicketAccountTypesCache.Store(&cachedOpenAICodexTicketAccountTypes{value: []string{"team"}, expiresAt: 0})
	values = s.GetOpenAICodexTicketAccountTypes(ctx)
	require.NotNil(t, values, "empty selection must serialize as [] rather than null")
	require.Empty(t, values)
	repo.err = errors.New("temporary database failure")
	s.InvalidateOpenAICodexTicketAccountTypesCache()
	require.Empty(t, s.GetOpenAICodexTicketAccountTypes(ctx), "a database error must not turn an empty selection into all plans")
	repo.err = nil
	delete(repo.values, key)
	s.InvalidateOpenAICodexTicketAccountTypesCache()
	require.Equal(t, DefaultOpenAICodexTicketAccountTypes(), s.GetOpenAICodexTicketAccountTypes(ctx))
}

func TestCodexTicketAccountTypeScopeControlsHarvestGateAndInjection(t *testing.T) {
	ctx := context.Background()
	key := SettingKeyOpenAICodexTicketAccountTypes
	settingsRepo := &codexTicketSettingRepo{codexPolicyMigrationRepoStub: &codexPolicyMigrationRepoStub{values: map[string]string{
		SettingKeyOpenAICodexTicketEnabled: "true", key: `["plus"]`,
	}}}
	settings := NewSettingService(settingsRepo, &config.Config{})
	upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		resp := codexTicketResponse()
		resp.Header.Set(openAICodexTurnStateHeader, fakeCodexTicketState(780))
		return resp, nil
	}}
	calls := 0
	counting := &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) { calls++; return upstream.do(req) }}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true,
		Models: []string{"gpt-6-astra"}, HarvestProxyURL: "http://proxy.example:8080"}, counting)
	svc.settingService = settings
	account := ticketTestAccount(41)
	account.Status = StatusActive
	account.Credentials["plan_type"] = "self_serve_business_prolite"
	svc.accountRepo = &codexTicketRefreshRepo{accounts: []Account{*account}}

	require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	svc.refreshOpenAICodexTickets(ctx)
	svc.probeOnceOpenAICodexTicket(ctx, account, "gpt-6-astra")
	require.Zero(t, calls)
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "client-state")
	require.NoError(t, svc.applyOpenAICodexTicket(ctx, account, "gpt-6-astra", h))
	require.Equal(t, "client-state", h.Get(openAICodexTurnStateHeader))

	settingsRepo.values[key] = `["self_serve_business_prolite"]`
	settings.InvalidateOpenAICodexTicketAccountTypesCache()
	require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	require.ErrorIs(t, svc.applyOpenAICodexTicket(ctx, account, "gpt-6-astra", h), ErrOpenAICodexTicketUnavailable)
	svc.refreshOpenAICodexTickets(ctx)
	require.Equal(t, 1, calls)
	require.NoError(t, svc.applyOpenAICodexTicket(ctx, account, "gpt-6-astra", h))
	require.Len(t, h.Get(openAICodexTurnStateHeader), 780)

	settingsRepo.values[key] = `[]`
	settings.InvalidateOpenAICodexTicketAccountTypesCache()
	h.Set(openAICodexTurnStateHeader, "client-state")
	require.NoError(t, svc.applyOpenAICodexTicket(ctx, account, "gpt-6-astra", h))
	require.Equal(t, "client-state", h.Get(openAICodexTurnStateHeader), "deselection must skip even cached tickets")
	svc.lookupOpenAICodexTicket(account, "gpt-6-astra").ExpiresAt = time.Now().Add(-time.Second)
	require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	svc.refreshOpenAICodexTickets(ctx)
	require.Equal(t, 1, calls)
}
