package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"hetman/internal/adapter"
)

// The client marks an assistant message that wraps an API error with a kind
// in "error" and, for some, a finer kind in "api_error"; the result event
// repeats "api_error" and the HTTP status. These typed kinds are read first,
// the message text only when there are none.
var (
	limitErrors     = []string{"rate_limit", "billing_error"}
	limitAPIErrors  = []string{"usage_limit_reached", "model_requires_usage_credits", "long_context_credits_required"}
	offlineAPIError = []string{"connection_failed", "network_edge_blocked"}
	permanentErrors = []string{"authentication_failed", "oauth_org_not_allowed", "account_on_hold",
		"verification_required", "model_not_found", "invalid_request", "cloud_credential_error"}
	permanentAPIErrors = []string{"claude_code_version_too_old", "stored_login_refused", "api_key_auth_disabled",
		"org_disabled_credential", "invalid_credential_header", "gateway_signin_required", "gateway_session_expired",
		"provider_credentials", "tls_untrusted_ca"}
)

var (
	limitText   = regexp.MustCompile(`(?i)hit your (?:\S+ )*limit|usage limit|rate limit|limit reached|out of usage`)
	offlineText = regexp.MustCompile(`(?i)ECONNREFUSED|ENOTFOUND|EAI_AGAIN|ETIMEDOUT|ENETUNREACH|EHOSTUNREACH|ECONNRESET|connection error|unable to connect|fetch failed`)
	authText    = regexp.MustCompile(`(?i)not logged in|please run /login|invalid api key|oauth token (?:has )?(?:expired|revoked)`)
	// Errors of the client's own argument checks, printed before it starts.
	argsText = regexp.MustCompile(`(?m)^error: |is not valid JSON|Input must be provided`)
	// An older form of the limit message ends with the reset time in Unix
	// seconds after a bar.
	resetText = regexp.MustCompile(`limit reached\|(\d{9,11})\b`)
)

func missingSession(text string) bool {
	return strings.Contains(text, "No conversation found with session ID")
}

func has(list []string, v string) bool {
	for _, x := range list {
		if v != "" && x == v {
			return true
		}
	}
	return false
}

// classify turns a run that ended without an answer into an error message.
func (s *stream) classify(exit int, stderr string) adapter.Message {
	var errs, apiErrs []string
	status := 0
	for _, ev := range s.apiErrors {
		errs = append(errs, ev.Error)
		apiErrs = append(apiErrs, ev.APIError)
		if ev.APIErrorStatus != nil {
			status = *ev.APIErrorStatus
		}
	}
	text := ""
	if r := s.result; r != nil {
		apiErrs = append(apiErrs, r.APIError)
		if r.APIErrorStatus != nil {
			status = *r.APIErrorStatus
		}
		text = strings.TrimSpace(r.Result)
		if text == "" {
			text = strings.Join(r.Errors, "; ")
		}
	}
	if text == "" {
		text = s.lastText
	}
	if text == "" {
		text = lastLine(stderr)
	}
	if text == "" {
		text = fmt.Sprintf("the client exited with code %d", exit)
	}
	text = clip(oneLine(text), 500)
	all := text + "\n" + stderr

	hit := func(list, values []string) bool {
		for _, v := range values {
			if has(list, v) {
				return true
			}
		}
		return false
	}
	switch {
	case hit(limitErrors, errs) || hit(limitAPIErrors, apiErrs) || status == 429:
		return s.limit(text, all)
	case hit(offlineAPIError, apiErrs):
		return failure(adapter.ErrOffline, text)
	case hit(permanentErrors, errs) || hit(permanentAPIErrors, apiErrs) || status == 401 || status == 403:
		return failure(adapter.ErrPermanent, text)
	case len(s.apiErrors) > 0 || status != 0:
		return failure(adapter.ErrTransient, text)
	case limitText.MatchString(all):
		return s.limit(text, all)
	case offlineText.MatchString(all):
		return failure(adapter.ErrOffline, text)
	case authText.MatchString(all):
		return failure(adapter.ErrPermanent, text)
	case s.result == nil && exit != 0 && argsText.MatchString(stderr):
		return failure(adapter.ErrPermanent, text)
	}
	return failure(adapter.ErrTransient, text)
}

// limit makes a limit error, with the reset time when the client gave one
// that is still ahead.
func (s *stream) limit(text, all string) adapter.Message {
	m := failure(adapter.ErrLimit, text)
	at := s.resetsAt
	if at == 0 {
		if g := resetText.FindStringSubmatch(all); g != nil {
			at, _ = strconv.ParseInt(g[1], 10, 64)
		}
	}
	if t := time.Unix(at, 0); at > 0 && t.After(time.Now()) {
		m.RetryAt = t.UTC().Format(time.RFC3339)
	}
	return m
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// extractObject finds the JSON object in a text answer: the last complete
// object in it, so that an answer that quotes an example before its final
// object gives the final one.
func extractObject(text string) (json.RawMessage, bool) {
	var last json.RawMessage
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(text[i:]))
		var raw json.RawMessage
		if dec.Decode(&raw) != nil {
			continue
		}
		if isObject(raw) {
			last = raw
			i += int(dec.InputOffset()) - 1
		}
	}
	return last, last != nil
}
