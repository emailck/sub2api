package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func ticketTestCookies() []*http.Cookie {
	return []*http.Cookie{{Name: "__oailb", Value: "route-a"}, {Name: "__cflb", Value: "lb-a"}, {Name: "__cf_bm", Value: "bot-a"}}
}
func ticketTestResponseHeaders() http.Header {
	h := http.Header{}
	for _, c := range ticketTestCookies() {
		h.Add("Set-Cookie", c.String()+"; Path=/; Secure; HttpOnly")
	}
	return h
}

func TestCodexTicketCookiesPersistAndInjectTogether(t *testing.T) {
	account := ticketTestAccount(41)
	repo := &codexTicketRefreshRepo{}
	upstream := &codexTicketFuncUpstream{do: func(req *http.Request) (*http.Response, error) {
		require.Empty(t, req.Header.Get("Cookie"), "each harvest starts a fresh bundle")
		resp := codexTicketResponse()
		resp.Header.Add("Set-Cookie", "__Secure-next-auth.session-token=private; Path=/; Secure")
		return resp, nil
	}}
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, HarvestProxyURL: "http://harvest.example:8080"}, upstream)
	svc.accountRepo = repo
	before := time.Now()
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	ticket := svc.lookupOpenAICodexTicket(account, "gpt-6-astra")
	require.NotNil(t, ticket)
	require.WithinDuration(t, before.Add(240*time.Second), ticket.ExpiresAt, time.Second)
	require.Equal(t, 240*time.Second, ticket.ExpiresAt.Sub(ticket.CapturedAt))
	require.Len(t, ticket.Cookies, 3)
	// JSON-backed hydration must retain the original values and absolute expiry.
	account.Extra = repo.updates
	restarted := ticketTestService(t, svc.cfg.Gateway.OpenAICodexTicket, nil)
	headers := http.Header{"Cookie": []string{"unrelated=downstream"}}
	require.NoError(t, restarted.applyOpenAICodexTicket(context.Background(), account, "gpt-6-astra", headers))
	require.Equal(t, ticket.State, headers.Get(openAICodexTurnStateHeader))
	require.Equal(t, "__oailb=route-a; __cflb=lb-a; __cf_bm=bot-a", headers.Get("Cookie"))
	require.True(t, ticket.ExpiresAt.Equal(restarted.lookupOpenAICodexTicket(account, "gpt-6-astra").ExpiresAt))
	require.Empty(t, RedactOpenAICodexTicketExtra(account.Extra))
	// Another model/account must not borrow this bundle.
	require.ErrorIs(t, restarted.applyOpenAICodexTicket(context.Background(), account, "gpt-5.6-sol", http.Header{}), ErrOpenAICodexTicketUnavailable)
	require.ErrorIs(t, restarted.applyOpenAICodexTicket(context.Background(), ticketTestAccount(42), "gpt-6-astra", http.Header{}), ErrOpenAICodexTicketUnavailable)
}

func TestCodexTicketCookiesScopeExpiryAndDeletion(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name  string
		extra []string
		ready bool
	}{
		{"complete", nil, true},
		{"unrelated domain cannot overwrite", []string{"__oailb=wrong; Domain=example.org; Path=/"}, true},
		{"unrelated path cannot overwrite", []string{"__oailb=wrong; Path=/unrelated"}, true},
		{"deletion", []string{"__oailb=; Max-Age=0; Path=/"}, false},
		{"expired", []string{"__cflb=old; Expires=Thu, 01 Jan 1970 00:00:01 GMT; Path=/"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{Header: ticketTestResponseHeaders()}
			for _, v := range tc.extra {
				resp.Header.Add("Set-Cookie", v)
			}
			ticket := &openAICodexTicket{State: fakeCodexTicketState(292), Length: 292, CapturedAt: now, ExpiresAt: now.Add(240 * time.Second), Cookies: captureOpenAICodexTicketCookies(resp, now)}
			require.Equal(t, tc.ready, ticket.valid(now, 292))
			if tc.ready {
				require.Contains(t, ticket.cookieHeader(now), "__oailb=route-a")
			}
		})
	}
	resp := &http.Response{Header: ticketTestResponseHeaders()}
	resp.Header.Add("Set-Cookie", "__oailb=short; Max-Age=30; Path=/")
	cookies := captureOpenAICodexTicketCookies(resp, now)
	ticket := &openAICodexTicket{State: fakeCodexTicketState(292), Length: 292, CapturedAt: now, ExpiresAt: now.Add(240 * time.Second), Cookies: cookies}
	require.True(t, ticket.valid(now.Add(29*time.Second), 292))
	require.False(t, ticket.valid(now.Add(30*time.Second), 292))
	require.Equal(t, now.Add(30*time.Second), ticket.effectiveExpiresAt())
	reloaded := parseOpenAICodexTicketFromAny(41, "gpt-6-astra", ticket)
	require.False(t, reloaded.valid(now.Add(30*time.Second), 292), "loading must not restart Max-Age")
}

func TestCodexTicketRefreshAt220ExpiresAt240(t *testing.T) {
	now := time.Now()
	ticket := &openAICodexTicket{State: fakeCodexTicketState(292), Length: 292, CapturedAt: now, ExpiresAt: now.Add(240 * time.Second), Cookies: ticketTestCookies()}
	cfg := ticketTestService(t, config.OpenAICodexTicketConfig{TTLSeconds: 3600, RefreshBeforeSeconds: 600}, nil).openAICodexTicketConfig()
	require.Equal(t, 240, cfg.TTLSeconds)
	require.Equal(t, 20, cfg.RefreshBeforeSeconds)
	require.False(t, ticket.needsRefresh(now.Add(219*time.Second), 20*time.Second))
	require.True(t, ticket.needsRefresh(now.Add(220*time.Second), 20*time.Second))
	require.True(t, ticket.valid(now.Add(239*time.Second), 292))
	require.False(t, ticket.valid(now.Add(240*time.Second), 292))
	ticket.ExpiresAt = now.Add(time.Hour)
	require.False(t, ticket.valid(now.Add(240*time.Second), 292), "legacy long expiry cannot extend the state")
	ticket.Cookies = nil
	require.False(t, ticket.valid(now, 292), "legacy tickets without cookies must be reacquired")
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{}, nil)
	ticket.Cookies = ticketTestCookies()
	svc.openaiCodexTickets.Store("test", ticket)
	require.Equal(t, time.Second, svc.nextOpenAICodexTicketHarvestDelay(now.Add(219*time.Second)))
}

func TestCodexTicketMissingCookieDoesNotReplaceGoodBundle(t *testing.T) {
	account := ticketTestAccount(41)
	svc := ticketTestService(t, config.OpenAICodexTicketConfig{Enabled: true, FailClosed: true, HarvestProxyURL: "http://harvest.example:8080"}, &codexTicketFuncUpstream{do: func(*http.Request) (*http.Response, error) {
		resp := codexTicketResponse()
		resp.Header.Del("Set-Cookie")
		return resp, nil
	}})
	good := &openAICodexTicket{Model: "gpt-6-astra", State: fakeCodexTicketState(292), Length: 292, CapturedAt: time.Now(), ExpiresAt: time.Now().Add(10 * time.Second), Cookies: ticketTestCookies()}
	svc.storeOpenAICodexTicket(context.Background(), account, good)
	svc.probeOnceOpenAICodexTicket(context.Background(), account, "gpt-6-astra")
	require.Same(t, good, svc.lookupOpenAICodexTicket(account, "gpt-6-astra"))
	require.True(t, good.valid(time.Now(), 292))
}

func TestCodexTicketWSBundleChangesHandshakeCompatibility(t *testing.T) {
	account := ticketTestAccount(41)
	headers := http.Header{}
	headers.Set(openAICodexTurnStateHeader, "ticket-one")
	headers.Set("Cookie", "__oailb=one; __cflb=lb; __cf_bm=bot")
	first := normalizeOpenAIWSHandshakeCompatibility(account, headers)
	require.Equal(t, first, normalizeOpenAIWSHandshakeCompatibility(account, headers.Clone()))
	headers.Set("Cookie", "__oailb=two; __cflb=lb; __cf_bm=bot")
	require.NotEqual(t, first, normalizeOpenAIWSHandshakeCompatibility(account, headers))
	headers.Set("Cookie", "__oailb=one; __cflb=lb; __cf_bm=bot")
	headers.Set(openAICodexTurnStateHeader, "ticket-two")
	require.NotEqual(t, first, normalizeOpenAIWSHandshakeCompatibility(account, headers))
}
