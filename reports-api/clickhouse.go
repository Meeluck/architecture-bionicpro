package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type reportItem struct {
	ProsthesisID      string  `json:"prosthesis_id"`
	SerialNumber      string  `json:"serial_number"`
	PeriodStart       string  `json:"period_start"`
	EventsCount       uint64  `json:"events_count"`
	AvgResponseTimeMS float64 `json:"avg_response_time_ms"`
	P95ResponseTimeMS float64 `json:"p95_response_time_ms"`
	AvgBatteryPercent float64 `json:"avg_battery_percent"`
	LastEventAt       string  `json:"last_event_at"`
}

type reportStore interface {
	processedUntil(context.Context) (time.Time, error)
	report(context.Context, string, time.Time, time.Time) ([]reportItem, error)
	ping(context.Context) error
}

type clickHouseStore struct {
	baseURL  string
	database string
	username string
	password string
	client   *http.Client
}

func newClickHouseStore(baseURL, database, username, password string, client *http.Client) *clickHouseStore {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &clickHouseStore{
		baseURL: strings.TrimRight(baseURL, "/"), database: database,
		username: username, password: password, client: client,
	}
}

func (s *clickHouseStore) processedUntil(ctx context.Context) (time.Time, error) {
	body, err := s.query(ctx, `
SELECT
    count() AS state_rows,
    formatDateTime(max(processed_until), '%Y-%m-%dT%H:%i:%SZ', 'UTC') AS processed_until
FROM etl_state
WHERE pipeline = {pipeline:String}
FORMAT TSVRaw`, url.Values{"param_pipeline": {"reports_etl"}})
	if err != nil {
		return time.Time{}, err
	}
	fields := strings.Split(strings.TrimSpace(string(body)), "\t")
	if len(fields) != 2 || fields[0] == "0" {
		return time.Time{}, errorsNoProcessedData
	}
	processedUntil, err := time.Parse(time.RFC3339, fields[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("parse processed_until: %w", err)
	}
	return processedUntil, nil
}

func (s *clickHouseStore) report(ctx context.Context, userID string, from, to time.Time) ([]reportItem, error) {
	body, err := s.query(ctx, `
SELECT
    toString(mart.prosthesis_id) AS prosthesis_id,
    mart.serial_number,
    formatDateTime(mart.period_start, '%Y-%m-%dT%H:%i:%SZ', 'UTC') AS period_start,
    mart.events_count,
    mart.avg_response_time_ms,
    mart.p95_response_time_ms,
    mart.avg_battery_percent,
    formatDateTime(mart.last_event_at, '%Y-%m-%dT%H:%i:%SZ', 'UTC') AS last_event_at
FROM report_by_user_hour AS mart
WHERE mart.user_id = {user_id:UUID}
  AND mart.period_start >= {from:DateTime}
  AND mart.period_start < {to:DateTime}
ORDER BY mart.period_start, mart.prosthesis_id
FORMAT JSONEachRow`,
		url.Values{
			"param_user_id": {userID},
			"param_from":    {from.UTC().Format("2006-01-02 15:04:05")},
			"param_to":      {to.UTC().Format("2006-01-02 15:04:05")},
			"output_format_json_quote_64bit_integers": {"0"},
		})
	if err != nil {
		return nil, err
	}

	items := make([]reportItem, 0)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 1<<20)
	for scanner.Scan() {
		var item reportItem
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			return nil, fmt.Errorf("decode ClickHouse report row: %w", err)
		}
		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read ClickHouse report: %w", err)
	}
	return items, nil
}

func (s *clickHouseStore) ping(ctx context.Context) error {
	_, err := s.query(ctx, "SELECT 1 FORMAT TSVRaw", nil)
	return err
}

func (s *clickHouseStore) query(ctx context.Context, sql string, parameters url.Values) ([]byte, error) {
	endpoint, err := url.Parse(s.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse ClickHouse URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("database", s.database)
	for name, values := range parameters {
		for _, value := range values {
			query.Add(name, value)
		}
	}
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), strings.NewReader(sql))
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(s.username, s.password)
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("query ClickHouse: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("read ClickHouse response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ClickHouse returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
}

var errorsNoProcessedData = fmt.Errorf("no processed report interval")
