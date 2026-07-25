package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testIssuer  = "http://keycloak.test/realms/reports-realm"
	testUserID  = "44a50ba4-d96c-4c5c-b0f6-602069c63aa6"
	otherUserID = "d73fbadb-e292-4d3f-baca-749b54d9debe"
)

type fakeReportStore struct {
	processed      time.Time
	items          []reportItem
	processedCalls int
	reportCalls    int
	reportUserID   string
}

func (s *fakeReportStore) processedUntil(context.Context) (time.Time, error) {
	s.processedCalls++
	return s.processed, nil
}

func (s *fakeReportStore) report(_ context.Context, userID string, _, _ time.Time) ([]reportItem, error) {
	s.reportCalls++
	s.reportUserID = userID
	return s.items, nil
}

func (s *fakeReportStore) ping(context.Context) error { return nil }

type testAuth struct {
	privateKey *rsa.PrivateKey
	keyID      string
	jwksClient *http.Client
	now        time.Time
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func newTestAuth(t *testing.T) *testAuth {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyID := "test-key"
	exponent := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes())
	modulus := base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes())
	jwksDocument, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kid": keyID, "kty": "RSA", "alg": "RS256", "use": "sig", "n": modulus, "e": exponent,
	}}})
	jwksClient := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(jwksDocument))),
		}, nil
	})}
	return &testAuth{privateKey: privateKey, keyID: keyID, jwksClient: jwksClient, now: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)}
}

func (a *testAuth) verifier() *tokenVerifier {
	verifier := newTokenVerifier(testIssuer, "reports-api", "prothetic_user", "http://jwks.test", a.jwksClient)
	verifier.now = func() time.Time { return a.now }
	return verifier
}

func (a *testAuth) token(t *testing.T, subject string, roles []string, aud audience) string {
	t.Helper()
	claims := tokenClaims{
		Subject: subject, Issuer: testIssuer, Audience: aud,
		ExpiresAt: a.now.Add(time.Hour).Unix(), IssuedAt: a.now.Add(-time.Minute).Unix(),
	}
	claims.RealmAccess.Roles = roles
	header := map[string]string{"alg": "RS256", "typ": "JWT", "kid": a.keyID}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	headerPart := base64.RawURLEncoding.EncodeToString(headerJSON)
	claimsPart := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := headerPart + "." + claimsPart
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, a.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func newTestServer(verifier *tokenVerifier, store reportStore) *apiServer {
	return &apiServer{
		verifier:           verifier,
		store:              store,
		logger:             slog.New(slog.NewTextHandler(&strings.Builder{}, nil)),
		allowedOrigin:      "http://localhost:3000",
		defaultReportRange: 30 * 24 * time.Hour,
		maxReportRange:     31 * 24 * time.Hour,
	}
}

func TestReportsReturnsOnlyAuthenticatedUsersData(t *testing.T) {
	auth := newTestAuth(t)
	store := &fakeReportStore{
		processed: auth.now,
		items:     []reportItem{{ProsthesisID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1", EventsCount: 12}},
	}
	server := newTestServer(auth.verifier(), store)
	token := auth.token(t, testUserID, []string{"prothetic_user"}, audience{"reports-api"})

	request := httptest.NewRequest(http.MethodGet, "/reports?user_id="+testUserID, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if store.reportCalls != 1 || store.reportUserID != testUserID {
		t.Fatalf("store queried with unexpected identity: calls=%d user=%q", store.reportCalls, store.reportUserID)
	}
	var payload reportResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.UserID != testUserID || len(payload.Items) != 1 {
		t.Fatalf("unexpected response: %+v", payload)
	}
}

func TestReportsRejectsForeignUserBeforeQueryingOLAP(t *testing.T) {
	auth := newTestAuth(t)
	store := &fakeReportStore{processed: auth.now}
	server := newTestServer(auth.verifier(), store)
	token := auth.token(t, testUserID, []string{"prothetic_user"}, audience{"reports-api"})

	request := httptest.NewRequest(http.MethodGet, "/reports?user_id="+otherUserID, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", response.Code, response.Body.String())
	}
	if store.processedCalls != 0 || store.reportCalls != 0 {
		t.Fatalf("OLAP must not be queried for a foreign user: processed=%d report=%d", store.processedCalls, store.reportCalls)
	}
}

func TestReportsRequiresRoleAndAudience(t *testing.T) {
	tests := []struct {
		name   string
		roles  []string
		aud    audience
		status int
	}{
		{name: "missing role", roles: []string{"user"}, aud: audience{"reports-api"}, status: http.StatusForbidden},
		{name: "wrong audience", roles: []string{"prothetic_user"}, aud: audience{"account"}, status: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auth := newTestAuth(t)
			store := &fakeReportStore{processed: auth.now}
			server := newTestServer(auth.verifier(), store)
			token := auth.token(t, testUserID, test.roles, test.aud)
			request := httptest.NewRequest(http.MethodGet, "/reports", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			server.routes().ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("expected %d, got %d: %s", test.status, response.Code, response.Body.String())
			}
			if store.reportCalls != 0 {
				t.Fatal("OLAP must not be queried for an unauthorized token")
			}
		})
	}
}

func TestReportsRejectsUnprocessedPeriod(t *testing.T) {
	auth := newTestAuth(t)
	store := &fakeReportStore{processed: auth.now}
	server := newTestServer(auth.verifier(), store)
	token := auth.token(t, testUserID, []string{"prothetic_user"}, audience{"reports-api"})

	request := httptest.NewRequest(http.MethodGet, "/reports?to="+auth.now.Add(time.Hour).Format(time.RFC3339), nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", response.Code, response.Body.String())
	}
	if store.reportCalls != 0 {
		t.Fatal("mart must not be queried for an unprocessed period")
	}
}
