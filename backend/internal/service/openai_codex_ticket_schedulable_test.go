package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexTicketHarvestRequiresSchedulableAccount(t *testing.T) {
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name    string
		disable func(*Account)
		restore func(*Account)
	}{
		{"manual switch", func(a *Account) { a.Schedulable = false }, func(a *Account) { a.Schedulable = true }},
		{"inactive", func(a *Account) { a.Status = StatusDisabled }, func(a *Account) { a.Status = StatusActive }},
		{"expired", func(a *Account) { a.AutoPauseOnExpired = true; a.ExpiresAt = &past }, func(a *Account) { a.ExpiresAt = &future }},
		{"rate limited", func(a *Account) { a.RateLimitResetAt = &future }, func(a *Account) { a.RateLimitResetAt = &past }},
		{"overloaded", func(a *Account) { a.OverloadUntil = &future }, func(a *Account) { a.OverloadUntil = &past }},
		{"temporary cooldown", func(a *Account) { a.TempUnschedulableUntil = &future }, func(a *Account) { a.TempUnschedulableUntil = &past }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := ticketTestAccount(41)
			tc.disable(account)
			calls := 0
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, Models: []string{"gpt-6-astra"}, HarvestProxyURL: "http://harvest.example:8080"}, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) { calls++; return codexTicketResponse(), nil }})
			repo := &codexTicketRefreshRepo{accounts: []Account{*account}}
			svc.accountRepo = repo
			svc.refreshOpenAICodexTickets(context.Background())
			svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
			require.Zero(t, calls, "both the scheduled loop and direct probe must skip unavailable accounts")
			require.Empty(t, repo.updates)
			tc.restore(account)
			repo.accounts = []Account{*account}
			svc.refreshOpenAICodexTickets(context.Background())
			require.Equal(t, 1, calls, "resuming scheduling must resume harvesting without requiring an existing ticket")
			require.NotNil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
			// A ticket nearing expiry must not be renewed after scheduling is disabled.
			svc.lookupOpenAICodexTicket(account, "gpt-6-astra").ExpiresAt = time.Now().Add(10 * time.Second)
			account.Schedulable = false
			repo.accounts = []Account{*account}
			svc.refreshOpenAICodexTickets(context.Background())
			require.Equal(t, 1, calls)
		})
	}
}
