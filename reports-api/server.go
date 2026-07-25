package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

type apiServer struct {
	verifier           *tokenVerifier
	store              reportStore
	logger             *slog.Logger
	allowedOrigin      string
	defaultReportRange time.Duration
	maxReportRange     time.Duration
}

type reportResponse struct {
	UserID         string       `json:"user_id"`
	From           string       `json:"from"`
	To             string       `json:"to"`
	ProcessedUntil string       `json:"processed_until"`
	Items          []reportItem `json:"items"`
}

func (s *apiServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/readyz", s.ready)
	mux.HandleFunc("/reports", s.reports)
	return s.cors(mux)
}

func (s *apiServer) health(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *apiServer) ready(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
	defer cancel()
	if err := s.store.ping(ctx); err != nil {
		writeError(response, http.StatusServiceUnavailable, "not_ready", "ClickHouse is unavailable")
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *apiServer) reports(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response)
		return
	}

	rawToken, ok := bearerToken(request.Header.Get("Authorization"))
	if !ok {
		writeError(response, http.StatusUnauthorized, "unauthorized", "Bearer access token is required")
		return
	}
	claims, err := s.verifier.verify(request.Context(), rawToken)
	if err != nil {
		if errors.Is(err, errInsufficientRole) {
			writeError(response, http.StatusForbidden, "forbidden", "Required report role is missing")
			return
		}
		writeError(response, http.StatusUnauthorized, "unauthorized", "Access token is invalid")
		return
	}
	if !uuidPattern.MatchString(claims.Subject) {
		writeError(response, http.StatusUnauthorized, "unauthorized", "Token subject is not a valid user id")
		return
	}

	requestedUserID := request.URL.Query().Get("user_id")
	if requestedUserID == "" {
		requestedUserID = claims.Subject
	}
	if requestedUserID != claims.Subject {
		writeError(response, http.StatusForbidden, "foreign_report_forbidden", "A user can request only their own report")
		return
	}

	processedUntil, err := s.store.processedUntil(request.Context())
	if err != nil {
		s.logger.Error("read processed report boundary", "error", err)
		writeError(response, http.StatusServiceUnavailable, "report_not_ready", "No processed report interval is available")
		return
	}
	from, to, err := s.reportPeriod(request, processedUntil)
	if err != nil {
		var apiErr *requestError
		if errors.As(err, &apiErr) {
			writeJSON(response, apiErr.status, apiErr.payload)
			return
		}
		writeError(response, http.StatusBadRequest, "invalid_period", err.Error())
		return
	}

	items, err := s.store.report(request.Context(), claims.Subject, from, to)
	if err != nil {
		s.logger.Error("read report mart", "error", err, "user_id", claims.Subject)
		writeError(response, http.StatusBadGateway, "olap_error", "Report storage request failed")
		return
	}
	writeJSON(response, http.StatusOK, reportResponse{
		UserID: claims.Subject, From: from.Format(time.RFC3339), To: to.Format(time.RFC3339),
		ProcessedUntil: processedUntil.Format(time.RFC3339), Items: items,
	})
}

func (s *apiServer) reportPeriod(request *http.Request, processedUntil time.Time) (time.Time, time.Time, error) {
	to := processedUntil.UTC()
	if raw := request.URL.Query().Get("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("to must be RFC3339")
		}
		to = parsed.UTC()
	}
	if to.After(processedUntil) {
		return time.Time{}, time.Time{}, &requestError{
			status: http.StatusUnprocessableEntity,
			payload: map[string]any{
				"error":           map[string]string{"code": "period_not_processed", "message": "Requested period is not processed yet"},
				"processed_until": processedUntil.Format(time.RFC3339),
			},
		}
	}

	from := to.Add(-s.defaultReportRange)
	if raw := request.URL.Query().Get("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("from must be RFC3339")
		}
		from = parsed.UTC()
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, errors.New("from must be earlier than to")
	}
	if to.Sub(from) > s.maxReportRange {
		return time.Time{}, time.Time{}, errors.New("requested period is too large")
	}
	return from, to, nil
}

func (s *apiServer) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Origin") == s.allowedOrigin {
			response.Header().Set("Access-Control-Allow-Origin", s.allowedOrigin)
			response.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			response.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			response.Header().Set("Vary", "Origin")
		}
		if request.Method == http.MethodOptions {
			response.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func bearerToken(header string) (string, bool) {
	parts := strings.Fields(header)
	returnValue := ""
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] != "" {
		returnValue = parts[1]
		return returnValue, true
	}
	return "", false
}

type requestError struct {
	status  int
	payload any
}

func (e *requestError) Error() string { return "request validation failed" }

func methodNotAllowed(response http.ResponseWriter) {
	response.Header().Set("Allow", "GET, OPTIONS")
	writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "Only GET is supported")
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}
