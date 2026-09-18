package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAICodexTicketPlanPolicyEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name, plan                       string
		personalTarget, teamTarget, want int
	}{
		{name: "team", plan: "team", want: 332},
		{name: "business", plan: "business", want: 332},
		{name: "business prolite", plan: "self_serve_business_prolite", want: 332},
		{name: "business usage based", plan: "self_serve_business_usage_based", want: 332},
		{name: "normalized team", plan: " Team ", want: 332},
		{name: "normalized prolite", plan: " SELF_SERVE_BUSINESS_PROLITE ", want: 332},
		{name: "plus", plan: "plus", want: 292},
		{name: "personal prolite", plan: "prolite", want: 292},
		{name: "unknown", want: 292},
		{name: "custom team", plan: "team", personalTarget: 300, teamTarget: 340, want: 340},
		{name: "custom personal", plan: "plus", personalTarget: 300, teamTarget: 340, want: 300},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true,
				TargetLength: tc.personalTarget, TeamTargetLength: tc.teamTarget,
				Models: []string{"gpt-6-astra"}, HarvestProxyURL: "http://harvest.example:8080"}
			account := ticketTestAccount(41)
			account.Credentials["plan_type"] = tc.plan
			account.Credentials["chatgpt_account_id"] = "workspace-team"
			repo := &codexTicketRefreshRepo{}
			upstream := &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
				require.Equal(t, "workspace-team", req.Header.Get("chatgpt-account-id"))
				resp := codexTicketResponse()
				resp.Header.Set(openAICodexTurnStateHeader, fakeCodexTicketState(tc.want))
				return resp, nil
			}}
			svc := ticketTestService(t, cfg, upstream)
			svc.accountRepo = repo
			require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
			require.ErrorIs(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", http.Header{}), ErrOpenAICodexTicketUnavailable)
			status := OpenAICodexTicketStatuses(account, cfg, time.Now())
			require.Len(t, status, 1)
			require.Equal(t, tc.want, status[0].TargetLength)
			require.True(t, status[0].Blocked)

			svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
			require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
			require.True(t, svc.openAICodexTicketBlocksAccount(ticketTestAccount(42), "gpt-6-astra"))
			h := http.Header{}
			require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h))
			require.Equal(t, fakeCodexTicketState(tc.want), h.Get(openAICodexTurnStateHeader))

			// A restart must hydrate the same plan-specific ticket from persistence.
			account.Extra = repo.updates
			restarted := ticketTestService(t, cfg, nil)
			require.False(t, restarted.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
			status = OpenAICodexTicketStatuses(account, cfg, time.Now())
			require.True(t, status[0].Ready)
			require.False(t, status[0].Blocked)
			require.Equal(t, tc.want, status[0].Length)
			require.Equal(t, tc.want, status[0].TargetLength)
		})
	}
}

func TestOpenAICodexTeamTicketRejectsWrongLengthAndFailedResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  string
		status int
	}{
		{"personal length", fakeCodexTicketState(292), 200},
		{"312 length", fakeCodexTicketState(312), 200},
		{"356 length", fakeCodexTicketState(356), 200},
		{"invalid prefix", strings.Repeat("X", 332), 200},
		{"unauthorized", fakeCodexTicketState(332), 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := ticketTestAccount(41)
			account.Credentials["plan_type"] = "self_serve_business_prolite"
			upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
				resp := codexTicketResponse()
				resp.StatusCode = tc.status
				resp.Header.Set(openAICodexTurnStateHeader, tc.state)
				return resp, nil
			}}
			svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, HarvestProxyURL: "http://harvest.example:8080"}, upstream)
			svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
			require.Nil(t, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
			require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
		})
	}
}

func TestOpenAICodexTeamTicketRefreshAndPlanChange(t *testing.T) {
	account := ticketTestAccount(41)
	account.Status = StatusActive
	account.Credentials["plan_type"] = "self_serve_business_prolite"
	repo := &codexTicketRefreshRepo{accounts: []Account{*account}}
	calls, length := 0, 332
	upstream := &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		calls++
		resp := codexTicketResponse()
		resp.Header.Set(openAICodexTurnStateHeader, fakeCodexTicketState(length))
		return resp, nil
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true,
		Models: []string{"gpt-6-astra"}, HarvestProxyURL: "http://harvest.example:8080"}, upstream)
	svc.accountRepo = repo
	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, 1, calls)
	require.False(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, 1, calls, "a fresh 332 ticket must not be harvested again")
	ticket := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	ticket.ExpiresAt = time.Now().Add(5 * time.Minute)
	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, 2, calls, "a near-expiry Team ticket must refresh")
	ticket = svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	ticket.ExpiresAt = time.Now().Add(-time.Second)
	require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"))
	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, 3, calls)

	account.Credentials["plan_type"] = "plus"
	length = 292
	require.True(t, svc.openAICodexTicketBlocksAccount(account, "gpt-6-astra"), "a prior Team ticket must not satisfy the personal policy")
	svc.refreshOpenAICodexTickets(context.Background())
	require.Equal(t, 4, calls)
	h := http.Header{}
	require.NoError(t, svc.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", h))
	require.Len(t, h.Get(openAICodexTurnStateHeader), 292)
}
