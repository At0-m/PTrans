package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

var ErrInvalidToken = errors.New("invalid or expired token")

type Config struct {
	Secret   string
	Issuer   string
	Audience string
}

type Authenticator struct {
	cfg Config
	now func() time.Time
}

type claims struct {
	Subject   string `json:"sub"`
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	ExpiresAt int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
	NotBefore int64  `json:"nbf"`
}

type header struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
}

func New(cfg Config) (*Authenticator, error) {
	if len(cfg.Secret) < 32 || strings.TrimSpace(cfg.Issuer) == "" || strings.TrimSpace(cfg.Audience) == "" {
		return nil, errors.New("JWT secret must contain at least 32 bytes; issuer and audience are required")
	}
	return &Authenticator{cfg: cfg, now: time.Now}, nil
}

func (a *Authenticator) Issue(subject string, ttl time.Duration) (string, error) {
	if !validSubject(subject) || ttl < time.Second || ttl > 24*time.Hour {
		return "", errors.New("invalid subject or token lifetime")
	}
	now := a.now().Unix()
	h, err := json.Marshal(header{Algorithm: "HS256", Type: "JWT"})
	if err != nil {
		return "", err
	}
	c, err := json.Marshal(claims{Subject: subject, Issuer: a.cfg.Issuer, Audience: a.cfg.Audience,
		ExpiresAt: now + int64(ttl/time.Second), IssuedAt: now, NotBefore: now})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	return payload + "." + base64.RawURLEncoding.EncodeToString(a.sign(payload)), nil
}

func (a *Authenticator) Authenticate(token string) (string, error) {
	if len(token) == 0 || len(token) > 4096 {
		return "", ErrInvalidToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", ErrInvalidToken
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, a.sign(parts[0]+"."+parts[1])) {
		return "", ErrInvalidToken
	}
	hraw, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		return "", ErrInvalidToken
	}
	var h header
	if strictJSON(hraw, &h) != nil || h.Algorithm != "HS256" || h.Type != "JWT" {
		return "", ErrInvalidToken
	}
	craw, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return "", ErrInvalidToken
	}
	var c claims
	if strictJSON(craw, &c) != nil {
		return "", ErrInvalidToken
	}
	now := a.now().Unix()
	if !validSubject(c.Subject) || c.Issuer != a.cfg.Issuer || c.Audience != a.cfg.Audience ||
		c.ExpiresAt <= now || c.IssuedAt <= 0 || c.IssuedAt > now || c.NotBefore <= 0 ||
		c.NotBefore > now || c.ExpiresAt <= c.IssuedAt || c.ExpiresAt <= c.NotBefore {
		return "", ErrInvalidToken
	}
	return c.Subject, nil
}

func (a *Authenticator) sign(payload string) []byte {
	m := hmac.New(sha256.New, []byte(a.cfg.Secret))
	_, _ = m.Write([]byte(payload))
	return m.Sum(nil)
}

func validSubject(s string) bool {
	if len(s) < 1 || len(s) > 128 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func strictJSON(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrInvalidToken
	}
	return nil
}
