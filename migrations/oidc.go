package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const githubIssuer = "https://token.actions.githubusercontent.com"

type claims struct {
	Issuer         string `json:"iss"`
	Audience       string `json:"aud"`
	Expiry         int64  `json:"exp"`
	NotBefore      int64  `json:"nbf"`
	IssuedAt       int64  `json:"iat"`
	RepositoryID   string `json:"repository_id"`
	Repository     string `json:"repository"`
	RunID          string `json:"run_id"`
	RunAttempt     string `json:"run_attempt"`
	SHA            string `json:"sha"`
	Actor          string `json:"actor"`
	Environment    string `json:"environment"`
	EventName      string `json:"event_name"`
	WorkflowRef    string `json:"workflow_ref"`
	JobWorkflowRef string `json:"job_workflow_ref"`
	JobWorkflowSHA string `json:"job_workflow_sha"`
}

type oidcVerifier struct {
	audience, jwksURL string
	client            *http.Client
	mu                sync.Mutex
	keys              map[string]*rsa.PublicKey
	fetched           time.Time
}

// Only GitHub's fixed JWKS endpoint is consulted. Token headers cannot select
// an issuer/key URL. Cache refresh is bounded even for unknown attacker kids.
func (v *oidcVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if time.Since(v.fetched) > time.Minute {
		req, err := http.NewRequestWithContext(ctx, "GET", v.jwksURL, nil)
		if err != nil {
			return nil, err
		}
		res, err := v.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return nil, errors.New("JWKS unavailable")
		}
		var doc struct {
			Keys []struct{ Kty, Kid, Alg, Use, N, E string }
		}
		if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&doc); err != nil {
			return nil, err
		}
		keys := map[string]*rsa.PublicKey{}
		for _, k := range doc.Keys {
			if k.Kty != "RSA" || k.Alg != "RS256" || k.Use != "sig" {
				continue
			}
			n, errN := base64.RawURLEncoding.DecodeString(k.N)
			e, errE := base64.RawURLEncoding.DecodeString(k.E)
			exponent := new(big.Int).SetBytes(e)
			if errN != nil || errE != nil || len(n) < 256 || !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64() > 1<<31-1 {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(exponent.Int64())}
		}
		v.keys = keys
		v.fetched = time.Now()
	}
	k := v.keys[kid]
	if k == nil {
		return nil, errors.New("unknown signing key")
	}
	return k, nil
}

func (v *oidcVerifier) verify(ctx context.Context, token string) (claims, error) {
	var c claims
	bad := errors.New("invalid GitHub identity")
	if len(token) > 32768 {
		return c, bad
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, bad
	}
	h, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return c, bad
	}
	var header struct{ Alg, Kid string }
	if json.Unmarshal(h, &header) != nil || header.Alg != "RS256" || header.Kid == "" {
		return c, bad
	}
	key, err := v.key(ctx, header.Kid)
	if err != nil {
		return c, bad
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return c, bad
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig) != nil {
		return c, bad
	}
	p, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(p, &c) != nil {
		return c, bad
	}
	now := time.Now().Unix()
	if c.Issuer != githubIssuer || c.Audience != v.audience || c.Expiry <= now || c.NotBefore == 0 || c.NotBefore > now || c.IssuedAt == 0 || c.IssuedAt > now+30 {
		return claims{}, bad
	}
	return c, nil
}
