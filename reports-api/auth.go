package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	errInvalidToken     = errors.New("invalid access token")
	errInsufficientRole = errors.New("required role is missing")
)

type audience []string

func (a *audience) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*a = audience{single}
		return nil
	}
	var multiple []string
	if err := json.Unmarshal(data, &multiple); err != nil {
		return fmt.Errorf("decode audience: %w", err)
	}
	*a = multiple
	return nil
}

func (a audience) contains(expected string) bool {
	for _, value := range a {
		if value == expected {
			return true
		}
	}
	return false
}

type tokenClaims struct {
	Subject         string   `json:"sub"`
	Issuer          string   `json:"iss"`
	Audience        audience `json:"aud"`
	ExpiresAt       int64    `json:"exp"`
	NotBefore       int64    `json:"nbf"`
	IssuedAt        int64    `json:"iat"`
	AuthorizedParty string   `json:"azp"`
	RealmAccess     struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

func (c tokenClaims) hasRole(required string) bool {
	if required == "" {
		return true
	}
	for _, role := range c.RealmAccess.Roles {
		if role == required {
			return true
		}
	}
	return false
}

type tokenVerifier struct {
	issuer       string
	audience     string
	requiredRole string
	keys         *jwksCache
	now          func() time.Time
}

func newTokenVerifier(issuer, expectedAudience, requiredRole, jwksURL string, client *http.Client) *tokenVerifier {
	return &tokenVerifier{
		issuer:       strings.TrimRight(issuer, "/"),
		audience:     expectedAudience,
		requiredRole: requiredRole,
		keys:         newJWKSCache(jwksURL, client, 15*time.Minute),
		now:          time.Now,
	}
}

func (v *tokenVerifier) verify(ctx context.Context, raw string) (tokenClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return tokenClaims{}, errInvalidToken
	}

	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	if err := decodeJWTPart(parts[0], &header); err != nil || header.Algorithm != "RS256" || header.KeyID == "" {
		return tokenClaims{}, errInvalidToken
	}

	var claims tokenClaims
	if err := decodeJWTPart(parts[1], &claims); err != nil {
		return tokenClaims{}, errInvalidToken
	}

	key, err := v.keys.key(ctx, header.KeyID)
	if err != nil {
		return tokenClaims{}, fmt.Errorf("%w: signing key unavailable", errInvalidToken)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return tokenClaims{}, errInvalidToken
	}

	now := v.now().Unix()
	const clockSkew = int64(30)
	if claims.Subject == "" || claims.Issuer != v.issuer || !claims.Audience.contains(v.audience) {
		return tokenClaims{}, errInvalidToken
	}
	if claims.ExpiresAt == 0 || now > claims.ExpiresAt+clockSkew {
		return tokenClaims{}, errInvalidToken
	}
	if claims.NotBefore != 0 && now+clockSkew < claims.NotBefore {
		return tokenClaims{}, errInvalidToken
	}
	if claims.IssuedAt != 0 && now+clockSkew < claims.IssuedAt {
		return tokenClaims{}, errInvalidToken
	}
	if !claims.hasRole(v.requiredRole) {
		return tokenClaims{}, errInsufficientRole
	}

	return claims, nil
}

func decodeJWTPart(raw string, target any) error {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(decoded, target); err != nil {
		return err
	}
	return nil
}

type jwksCache struct {
	url       string
	client    *http.Client
	ttl       time.Duration
	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	expiresAt time.Time
}

func newJWKSCache(url string, client *http.Client, ttl time.Duration) *jwksCache {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &jwksCache{url: url, client: client, ttl: ttl, keys: make(map[string]*rsa.PublicKey)}
}

func (c *jwksCache) key(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	key, found := c.keys[keyID]
	fresh := time.Now().Before(c.expiresAt)
	c.mu.RUnlock()
	if found && fresh {
		return key, nil
	}

	if err := c.refresh(ctx); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	key, found = c.keys[keyID]
	if !found {
		return nil, fmt.Errorf("JWKS key %q not found", keyID)
	}
	return key, nil
}

func (c *jwksCache) refresh(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS endpoint returned %s", response.Status)
	}

	var document struct {
		Keys []struct {
			KeyID     string `json:"kid"`
			KeyType   string `json:"kty"`
			Algorithm string `json:"alg"`
			Use       string `json:"use"`
			Modulus   string `json:"n"`
			Exponent  string `json:"e"`
		} `json:"keys"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey)
	for _, item := range document.Keys {
		if item.KeyID == "" || item.KeyType != "RSA" || item.Algorithm != "RS256" || item.Use != "sig" {
			continue
		}
		modulusBytes, err := base64.RawURLEncoding.DecodeString(item.Modulus)
		if err != nil {
			continue
		}
		exponentBytes, err := base64.RawURLEncoding.DecodeString(item.Exponent)
		if err != nil {
			continue
		}
		exponent := new(big.Int).SetBytes(exponentBytes)
		if !exponent.IsInt64() || exponent.Int64() <= 1 {
			continue
		}
		keys[item.KeyID] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulusBytes), E: int(exponent.Int64())}
	}
	if len(keys) == 0 {
		return errors.New("JWKS contains no supported signing keys")
	}
	c.keys = keys
	c.expiresAt = time.Now().Add(c.ttl)
	return nil
}
