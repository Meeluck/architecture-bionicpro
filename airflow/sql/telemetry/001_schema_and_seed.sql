CREATE TABLE IF NOT EXISTS telemetry (
    event_id UUID PRIMARY KEY,
    prosthesis_id UUID NOT NULL,
    event_time TIMESTAMPTZ NOT NULL,
    movement_type TEXT NOT NULL,
    response_time_ms INTEGER NOT NULL CHECK (response_time_ms >= 0),
    battery_percent INTEGER NOT NULL CHECK (battery_percent BETWEEN 0 AND 100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_telemetry_event_time
    ON telemetry (event_time, event_id);
CREATE INDEX IF NOT EXISTS idx_telemetry_prosthesis_time
    ON telemetry (prosthesis_id, event_time);

-- Recreate a deterministic 30-day history for four prostheses. One event is
-- generated every five minutes: 4 * 30 * 24 * 12 = 34,560 events.
TRUNCATE TABLE telemetry;

WITH prosthesis_seed AS (
    SELECT *
    FROM (
        VALUES
            ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1'::UUID, 1, 68),
            ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa2'::UUID, 2, 76),
            ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa3'::UUID, 3, 84),
            ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa4'::UUID, 4, 92)
    ) AS seed(prosthesis_id, prosthesis_number, base_response_time_ms)
),
generated_events AS (
    SELECT
        seed.prosthesis_id,
        seed.prosthesis_number,
        seed.base_response_time_ms,
        sequence_no,
        date_trunc('hour', CURRENT_TIMESTAMP)
            - INTERVAL '30 days'
            + sequence_no * INTERVAL '5 minutes'
            + seed.prosthesis_number * INTERVAL '10 seconds' AS event_time
    FROM prosthesis_seed AS seed
    CROSS JOIN generate_series(0, 8639) AS sequence_no
)
INSERT INTO telemetry (
    event_id,
    prosthesis_id,
    event_time,
    movement_type,
    response_time_ms,
    battery_percent,
    created_at
)
SELECT
    md5(prosthesis_id::TEXT || ':' || sequence_no::TEXT)::UUID,
    prosthesis_id,
    event_time,
    (ARRAY[
        'hand_open',
        'hand_close',
        'pinch',
        'point',
        'wrist_rotate'
    ])[1 + (sequence_no % 5)::INTEGER],
    (base_response_time_ms + sequence_no % 36)::INTEGER,
    (20 + (sequence_no + prosthesis_number * 11) % 81)::INTEGER,
    event_time + INTERVAL '1 second'
FROM generated_events;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'telemetry_reader') THEN
        CREATE USER telemetry_reader WITH PASSWORD 'telemetry_reader_password';
    END IF;
END
$$;

GRANT CONNECT ON DATABASE telemetry TO telemetry_reader;
GRANT USAGE ON SCHEMA public TO telemetry_reader;
GRANT SELECT ON telemetry TO telemetry_reader;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO telemetry_reader;

