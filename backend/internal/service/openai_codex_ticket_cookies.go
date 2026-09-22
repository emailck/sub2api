package service

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const (
	openAICodexTicketDefaultTTLSeconds     = 240
	openAICodexTicketMaxTTLSeconds         = 240
	openAICodexTicketDefaultRefreshSeconds = 20
)

// Only retain infrastructure cookies observed on the harvest endpoint. Account
// login cookies and downstream client cookies must never enter a ticket bundle.
func allowedOpenAICodexTicketCookie(name string) bool {
	switch name {
	case "__oailb", "__cflb", "__cf_bm":
		return true
	default:
		return false
	}
}

// Normalize Max-Age once, when received, so loading a persisted ticket cannot
// restart a cookie's lifetime. A per-probe jar enforces domain/path restrictions
// and duplicate/deletion semantics without sharing cookies across accounts.
func captureOpenAICodexTicketCookies(resp *http.Response, now time.Time) []*http.Cookie {
	u, _ := url.Parse(chatgptCodexURL)
	jar, _ := cookiejar.New(nil)
	expires := make(map[string]time.Time)
	for _, c := range resp.Cookies() {
		if !allowedOpenAICodexTicketCookie(c.Name) || c.Valid() != nil {
			continue
		}
		if c.MaxAge > 0 {
			c.Expires = now.Add(time.Duration(c.MaxAge) * time.Second)
			c.MaxAge = 0
		}
		// Only cookies applicable to the actual endpoint may affect the snapshot.
		check, _ := cookiejar.New(nil)
		check.SetCookies(u, []*http.Cookie{c})
		if len(check.Cookies(u)) > 0 {
			expires[c.Name] = c.Expires
		}
		jar.SetCookies(u, []*http.Cookie{c})
	}
	cookies := jar.Cookies(u)
	for _, c := range cookies {
		c.Expires = expires[c.Name]
	}
	return cookies
}

// These cookies are a snapshot belonging to this exact state, not an account-
// wide jar that a concurrent harvest for another model can overwrite.
func (t *openAICodexTicket) cookieHeader(now time.Time) string {
	if t == nil {
		return ""
	}
	parts := make([]string, 0, len(t.Cookies))
	seen := make(map[string]bool)
	for _, c := range t.Cookies {
		if c == nil || !allowedOpenAICodexTicketCookie(c.Name) || c.Value == "" || c.Valid() != nil || seen[c.Name] || c.MaxAge < 0 || (!c.Expires.IsZero() && !now.Before(c.Expires)) {
			return ""
		}
		seen[c.Name] = true
		parts = append(parts, (&http.Cookie{Name: c.Name, Value: c.Value}).String())
	}
	if !seen["__oailb"] || !seen["__cflb"] || !seen["__cf_bm"] {
		return ""
	}
	return strings.Join(parts, "; ")
}

// Bound old persisted expiries too; neither a restart nor a longer-lived cookie
// may extend the state beyond its 240-second lifetime.
func (t *openAICodexTicket) effectiveExpiresAt() time.Time {
	if t == nil {
		return time.Time{}
	}
	expires := t.ExpiresAt
	limit := t.CapturedAt.Add(openAICodexTicketMaxTTLSeconds * time.Second)
	if limit.Before(expires) {
		expires = limit
	}
	for _, cookie := range t.Cookies {
		if cookie != nil && !cookie.Expires.IsZero() && cookie.Expires.Before(expires) {
			expires = cookie.Expires
		}
	}
	return expires
}

// Wake at an upcoming refresh deadline instead of overshooting age 220 by a
// full polling interval. Missing tickets still retry at the configured interval.
func (s *OpenAIGatewayService) nextOpenAICodexTicketHarvestDelay(now time.Time) time.Duration {
	cfg := s.openAICodexTicketConfig()
	delay := time.Duration(cfg.HarvestProbeIntervalSeconds) * time.Second
	refreshBefore := time.Duration(cfg.RefreshBeforeSeconds) * time.Second
	s.openaiCodexTickets.Range(func(_, value any) bool {
		ticket, ok := value.(*openAICodexTicket)
		if !ok || ticket == nil {
			return true
		}
		due := ticket.effectiveExpiresAt().Add(-refreshBefore).Sub(now)
		if due > 0 && due < delay {
			delay = due
		}
		return true
	})
	return delay
}
