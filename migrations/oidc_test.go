package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOIDCSignatureAndClaims(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "test", "alg": "RS256", "use": "sig", "n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes())}}})
	}))
	defer jwks.Close()
	v := oidcVerifier{audience: "https://migrations.example", jwksURL: jwks.URL, client: jwks.Client()}
	token := func(change func(map[string]any)) string {
		payload := map[string]any{"iss": githubIssuer, "aud": v.audience, "exp": time.Now().Unix() + 300, "nbf": time.Now().Unix() - 5, "iat": time.Now().Unix(), "repository_id": "123"}
		change(payload)
		h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "test"})
		p, _ := json.Marshal(payload)
		msg := b64(h) + "." + b64(p)
		digest := sha256.Sum256([]byte(msg))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		return msg + "." + b64(sig)
	}
	valid := token(func(map[string]any) {})
	if _, err := v.verify(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"issuer", func(p map[string]any) { p["iss"] = "https://evil.example" }},
		{"audience", func(p map[string]any) { p["aud"] = "other" }},
		{"expired", func(p map[string]any) { p["exp"] = time.Now().Unix() - 1 }},
		{"not yet valid", func(p map[string]any) { p["nbf"] = time.Now().Unix() + 60 }},
		{"missing expiry", func(p map[string]any) { delete(p, "exp") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := v.verify(context.Background(), token(tc.change)); err == nil {
				t.Fatal("accepted invalid token")
			}
		})
	}
	if _, err := v.verify(context.Background(), valid[:len(valid)-10]+"AAAAAAAAAA"); err == nil {
		t.Fatal("accepted forged signature")
	}
	if _, err := v.verify(context.Background(), "eyJhbGciOiJub25lIn0.e30."); err == nil {
		t.Fatal("accepted unsigned token")
	}
}
