CREATE TABLE IF NOT EXISTS bionicpro.stg_user_prosthesis
(
    user_id UUID,
    user_name String,
    prosthesis_id UUID,
    serial_number String,
    active UInt8,
    source_updated_at DateTime64(3, 'UTC'),
    loaded_at DateTime64(3, 'UTC'),
    dag_run_id String
)
ENGINE = MergeTree
ORDER BY (prosthesis_id, user_id);

CREATE TABLE IF NOT EXISTS bionicpro.stg_telemetry
(
    event_id UUID,
    prosthesis_id UUID,
    event_time DateTime64(3, 'UTC'),
    movement_type LowCardinality(String),
    response_time_ms UInt32,
    battery_percent UInt8,
    loaded_at DateTime64(3, 'UTC'),
    dag_run_id String
)
ENGINE = ReplacingMergeTree(loaded_at)
PARTITION BY toYYYYMM(event_time)
ORDER BY event_id;

CREATE TABLE IF NOT EXISTS bionicpro.report_by_user_hour
(
    user_id UUID,
    user_name String,
    prosthesis_id UUID,
    serial_number String,
    period_start DateTime('UTC'),
    events_count UInt64,
    avg_response_time_ms Float64,
    p95_response_time_ms Float64,
    avg_battery_percent Float64,
    last_event_at DateTime64(3, 'UTC'),
    etl_batch_id String,
    updated_at DateTime64(3, 'UTC')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(period_start)
ORDER BY (user_id, period_start, prosthesis_id);

CREATE TABLE IF NOT EXISTS bionicpro.etl_state
(
    pipeline LowCardinality(String),
    processed_until DateTime64(3, 'UTC'),
    updated_at DateTime64(3, 'UTC'),
    dag_run_id String
)
ENGINE = ReplacingMergeTree(updated_at)
ORDER BY pipeline;

CREATE USER IF NOT EXISTS reports_api
IDENTIFIED WITH sha256_password BY 'reports_api_password';

GRANT SELECT ON bionicpro.report_by_user_hour TO reports_api;
GRANT SELECT ON bionicpro.etl_state TO reports_api;
