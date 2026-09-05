package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) *Authenticator {
	t.Helper()
	a, err := New(Config{Secret: "test-signing-secret-at-least-32-bytes", Issuer: "ptrans", Audience: "api"})
	if err != nil {
		t.Fatal(err)
	}
	a.now = func() time.Time { return time.Unix(1800000000, 0) }
	return a
}

func TestJWTIssueAndAuthenticate(t *testing.T) {
	a := fixture(t)
	token, err := a.Issue("alice", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := a.Authenticate(token)
	if err != nil || subject != "alice" {
		t.Fatalf("subject=%q error=%v", subject, err)
	}
	other := fixture(t)
	other.cfg.Secret = strings.Repeat("x", 32)
	if _, err := other.Authenticate(token); err == nil {
		t.Fatal("accepted wrong key")
	}
	a.now = func() time.Time { return time.Unix(1800003600, 0) }
	if _, err := a.Authenticate(token); err == nil {
		t.Fatal("accepted expired token")
	}
}

func TestJWTRejectsInvalidClaimsAndAlgorithms(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any, map[string]any)
	}{
		{"none", func(h, c map[string]any) { h["alg"] = "none" }},
		{"HS512", func(h, c map[string]any) { h["alg"] = "HS512" }},
		{"external_key", func(h, c map[string]any) { h["jku"] = "https://example.com/key" }},
		{"issuer", func(h, c map[string]any) { c["iss"] = "other" }},
		{"audience", func(h, c map[string]any) { c["aud"] = "other" }},
		{"empty_subject", func(h, c map[string]any) { c["sub"] = "" }},
		{"whitespace_subject", func(h, c map[string]any) { c["sub"] = " alice" }},
		{"missing_exp", func(h, c map[string]any) { delete(c, "exp") }},
		{"expired", func(h, c map[string]any) { c["exp"] = 1800000000 }},
		{"future_iat", func(h, c map[string]any) { c["iat"] = 1800000050 }},
		{"future_nbf", func(h, c map[string]any) { c["nbf"] = 1800000050 }},
		{"missing_iat", func(h, c map[string]any) { delete(c, "iat") }},
		{"missing_nbf", func(h, c map[string]any) { delete(c, "nbf") }},
		{"string_exp", func(h, c map[string]any) { c["exp"] = "1800003600" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := fixture(t)
			h := map[string]any{"alg": "HS256", "typ": "JWT"}
			c := map[string]any{"sub": "alice", "iss": "ptrans", "aud": "api", "exp": 1800003600, "iat": 1800000000, "nbf": 1800000000}
			test.mutate(h, c)
			hb, _ := json.Marshal(h)
			cb, _ := json.Marshal(c)
			payload := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
			token := payload + "." + base64.RawURLEncoding.EncodeToString(a.sign(payload))
			if _, err := a.Authenticate(token); err == nil {
				t.Fatal("invalid JWT accepted")
			}
		})
	}
}

func TestJWTMalformedAndConfiguration(t *testing.T) {
	a := fixture(t)
	for _, token := range []string{"", "a.b.c", "a.b", "a.b.c.d", strings.Repeat("x", 5000)} {
		if _, err := a.Authenticate(token); err == nil {
			t.Fatalf("accepted %q", token)
		}
	}
	token, _ := a.Issue("alice", time.Hour)
	parts := strings.Split(token, ".")
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"bob"}`))
	if _, err := a.Authenticate(strings.Join(parts, ".")); err == nil {
		t.Fatal("accepted tampered payload")
	}
	for _, cfg := range []Config{{Secret: "short", Issuer: "p", Audience: "a"}, {Secret: strings.Repeat("x", 32), Audience: "a"}, {Secret: strings.Repeat("x", 32), Issuer: "p"}} {
		if _, err := New(cfg); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
	if _, err := a.Issue("alice", 25*time.Hour); err == nil {
		t.Fatal("accepted excessive lifetime")
	}
}
