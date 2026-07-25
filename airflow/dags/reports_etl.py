from __future__ import annotations

import logging
import os
from datetime import datetime, timedelta, timezone
from typing import Any

from airflow.decorators import dag, task
from airflow.operators.python import get_current_context


LOGGER = logging.getLogger(__name__)

PIPELINE_NAME = "reports_etl"
UTC = timezone.utc
EPOCH = datetime(1970, 1, 1, tzinfo=UTC)
LATE_ARRIVAL_WINDOW = timedelta(hours=2)


def _postgres_connection(prefix: str):
    """Create a source PostgreSQL connection from container environment."""
    import psycopg2

    return psycopg2.connect(
        host=os.environ[f"{prefix}_DB_HOST"],
        port=int(os.environ[f"{prefix}_DB_PORT"]),
        dbname=os.environ[f"{prefix}_DB_NAME"],
        user=os.environ[f"{prefix}_DB_USER"],
        password=os.environ[f"{prefix}_DB_PASSWORD"],
        connect_timeout=10,
        application_name=PIPELINE_NAME,
    )


def _clickhouse_client():
    """Create the ClickHouse HTTP client used by ETL tasks."""
    import clickhouse_connect

    return clickhouse_connect.get_client(
        host=os.environ["CLICKHOUSE_HOST"],
        port=int(os.environ["CLICKHOUSE_HTTP_PORT"]),
        database=os.environ["CLICKHOUSE_DB"],
        username=os.environ["CLICKHOUSE_USER"],
        password=os.environ["CLICKHOUSE_PASSWORD"],
        connect_timeout=10,
        send_receive_timeout=120,
    )


def _as_utc(value: datetime) -> datetime:
    if value.tzinfo is None:
        return value.replace(tzinfo=UTC)
    return value.astimezone(UTC)


def _parse_datetime(value: str) -> datetime:
    return _as_utc(datetime.fromisoformat(value))


def _clickhouse_datetime(value: datetime) -> str:
    """Format an internal datetime for a ClickHouse DateTime64 literal."""
    return _as_utc(value).strftime("%Y-%m-%d %H:%M:%S.%f")


def _clickhouse_string(value: str) -> str:
    """Escape an internal string for a ClickHouse literal."""
    return value.replace("\\", "\\\\").replace("'", "\\'")


@dag(
    dag_id=PIPELINE_NAME,
    description="Build the BionicPRO report mart from CRM and telemetry data",
    schedule="0 * * * *",
    start_date=datetime(2026, 1, 1, tzinfo=UTC),
    catchup=False,
    max_active_runs=1,
    default_args={
        "owner": "bionicpro",
        "retries": 2,
        "retry_delay": timedelta(minutes=1),
    },
    tags=["bionicpro", "etl", "clickhouse", "reports"],
)
def reports_etl():
    """Extract CRM and telemetry data and publish an hourly user mart."""

    @task
    def check_sources() -> bool:
        """Fail early unless both sources and the OLAP database are available."""
        with _postgres_connection("CRM") as connection:
            with connection.cursor() as cursor:
                cursor.execute("SELECT count(*) FROM users")
                crm_users = cursor.fetchone()[0]

        with _postgres_connection("TELEMETRY") as connection:
            with connection.cursor() as cursor:
                cursor.execute("SELECT count(*) FROM telemetry")
                telemetry_events = cursor.fetchone()[0]

        client = _clickhouse_client()
        try:
            clickhouse_ok = client.query("SELECT 1").result_rows[0][0]
        finally:
            client.close()

        if clickhouse_ok != 1:
            raise RuntimeError("ClickHouse availability check returned an error")

        LOGGER.info(
            "Sources are ready: crm_users=%s, telemetry_events=%s",
            crm_users,
            telemetry_events,
        )
        return True

    @task
    def sync_crm_snapshot(sources_ready: bool) -> dict[str, int]:
        """Replace the small current user-to-prosthesis dimension snapshot."""
        if not sources_ready:
            raise RuntimeError("Source availability check did not pass")

        with _postgres_connection("CRM") as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    """
                    SELECT
                        u.user_id,
                        u.full_name,
                        p.prosthesis_id,
                        p.serial_number,
                        p.active,
                        GREATEST(u.updated_at, p.updated_at) AS source_updated_at
                    FROM users AS u
                    INNER JOIN prostheses AS p ON p.user_id = u.user_id
                    ORDER BY p.prosthesis_id
                    """
                )
                source_rows = cursor.fetchall()

        context = get_current_context()
        run_id = context["run_id"]
        loaded_at = datetime.now(UTC)
        clickhouse_rows = [
            (
                user_id,
                full_name,
                prosthesis_id,
                serial_number,
                int(active),
                _as_utc(source_updated_at),
                loaded_at,
                run_id,
            )
            for (
                user_id,
                full_name,
                prosthesis_id,
                serial_number,
                active,
                source_updated_at,
            ) in source_rows
        ]

        client = _clickhouse_client()
        try:
            # The CRM dimension is deliberately a complete, small snapshot.
            # max_active_runs=1 prevents concurrent truncate/insert operations.
            client.command("TRUNCATE TABLE stg_user_prosthesis")
            if clickhouse_rows:
                client.insert(
                    "stg_user_prosthesis",
                    clickhouse_rows,
                    column_names=[
                        "user_id",
                        "user_name",
                        "prosthesis_id",
                        "serial_number",
                        "active",
                        "source_updated_at",
                        "loaded_at",
                        "dag_run_id",
                    ],
                )
        finally:
            client.close()

        LOGGER.info("Loaded %s CRM dimension rows", len(clickhouse_rows))
        return {"rows": len(clickhouse_rows)}

    @task
    def sync_telemetry_increment(sources_ready: bool) -> dict[str, Any]:
        """Load new telemetry plus a short overlap for late-arriving events."""
        if not sources_ready:
            raise RuntimeError("Source availability check did not pass")

        client = _clickhouse_client()
        try:
            result = client.query(
                """
                SELECT max(processed_until)
                FROM etl_state
                WHERE pipeline = %(pipeline)s
                """,
                parameters={"pipeline": PIPELINE_NAME},
            )
            previous_watermark = result.result_rows[0][0]
        finally:
            client.close()

        if previous_watermark is None:
            previous_watermark = EPOCH
        else:
            previous_watermark = _as_utc(previous_watermark)

        # Only fully closed hours are published as complete reports.
        cutoff = datetime.now(UTC).replace(minute=0, second=0, microsecond=0)
        lower_bound = max(EPOCH, previous_watermark - LATE_ARRIVAL_WINDOW)

        with _postgres_connection("TELEMETRY") as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    """
                    SELECT
                        event_id,
                        prosthesis_id,
                        event_time,
                        movement_type,
                        response_time_ms,
                        battery_percent
                    FROM telemetry
                    WHERE event_time >= %s
                      AND event_time < %s
                    ORDER BY event_time, event_id
                    """,
                    (lower_bound, cutoff),
                )
                source_rows = cursor.fetchall()

        context = get_current_context()
        run_id = context["run_id"]
        loaded_at = datetime.now(UTC)
        clickhouse_rows = [
            (
                event_id,
                prosthesis_id,
                _as_utc(event_time),
                movement_type,
                response_time_ms,
                battery_percent,
                loaded_at,
                run_id,
            )
            for (
                event_id,
                prosthesis_id,
                event_time,
                movement_type,
                response_time_ms,
                battery_percent,
            ) in source_rows
        ]

        client = _clickhouse_client()
        try:
            if clickhouse_rows:
                client.insert(
                    "stg_telemetry",
                    clickhouse_rows,
                    column_names=[
                        "event_id",
                        "prosthesis_id",
                        "event_time",
                        "movement_type",
                        "response_time_ms",
                        "battery_percent",
                        "loaded_at",
                        "dag_run_id",
                    ],
                )
        finally:
            client.close()

        LOGGER.info(
            "Loaded %s telemetry rows for [%s, %s)",
            len(clickhouse_rows),
            lower_bound.isoformat(),
            cutoff.isoformat(),
        )
        return {
            "from": lower_bound.isoformat(),
            "until": cutoff.isoformat(),
            "rows": len(clickhouse_rows),
            "previous_watermark": previous_watermark.isoformat(),
        }

    @task
    def build_report_mart(
        crm_result: dict[str, int],
        telemetry_window: dict[str, Any],
    ) -> dict[str, int]:
        """Idempotently recalculate aggregates in the affected time window."""
        if crm_result["rows"] == 0:
            raise ValueError("CRM snapshot is empty; refusing to publish the mart")

        lower_bound = _parse_datetime(telemetry_window["from"])
        cutoff = _parse_datetime(telemetry_window["until"])
        start_sql = _clickhouse_datetime(lower_bound)
        cutoff_sql = _clickhouse_datetime(cutoff)
        run_id = _clickhouse_string(get_current_context()["run_id"])

        client = _clickhouse_client()
        try:
            # A synchronous mutation followed by INSERT makes a retry for the
            # same window deterministic and prevents duplicate aggregates.
            client.command(
                f"""
                ALTER TABLE report_by_user_hour
                DELETE WHERE period_start >= toDateTime64('{start_sql}', 3, 'UTC')
                  AND period_start < toDateTime64('{cutoff_sql}', 3, 'UTC')
                SETTINGS mutations_sync = 2
                """
            )
            client.command(
                f"""
                INSERT INTO report_by_user_hour
                SELECT
                    p.user_id,
                    p.user_name,
                    t.prosthesis_id,
                    p.serial_number,
                    toStartOfHour(t.latest_event_time) AS period_start,
                    count() AS events_count,
                    avg(t.response_time_ms) AS avg_response_time_ms,
                    quantileExact(0.95)(t.response_time_ms) AS p95_response_time_ms,
                    avg(t.battery_percent) AS avg_battery_percent,
                    max(t.latest_event_time) AS last_event_at,
                    '{run_id}' AS etl_batch_id,
                    now64(3, 'UTC') AS updated_at
                FROM
                (
                    SELECT
                        event_id,
                        argMax(prosthesis_id, loaded_at) AS prosthesis_id,
                        argMax(event_time, loaded_at) AS latest_event_time,
                        argMax(response_time_ms, loaded_at) AS response_time_ms,
                        argMax(battery_percent, loaded_at) AS battery_percent
                    FROM stg_telemetry
                    WHERE event_time >= toDateTime64('{start_sql}', 3, 'UTC')
                      AND event_time < toDateTime64('{cutoff_sql}', 3, 'UTC')
                    GROUP BY event_id
                ) AS t
                INNER JOIN stg_user_prosthesis AS p
                    ON p.prosthesis_id = t.prosthesis_id
                   AND p.active = 1
                GROUP BY
                    p.user_id,
                    p.user_name,
                    t.prosthesis_id,
                    p.serial_number,
                    period_start
                """
            )
            mart_rows = client.query(
                f"""
                SELECT count()
                FROM report_by_user_hour
                WHERE period_start >= toDateTime64('{start_sql}', 3, 'UTC')
                  AND period_start < toDateTime64('{cutoff_sql}', 3, 'UTC')
                """
            ).result_rows[0][0]
        finally:
            client.close()

        LOGGER.info("Published %s mart rows", mart_rows)
        return {"rows": mart_rows}

    @task
    def validate_report_mart(
        telemetry_window: dict[str, Any],
        mart_result: dict[str, int],
    ) -> dict[str, int]:
        """Check ownership, event totals and uniqueness before watermarking."""
        lower_bound = _parse_datetime(telemetry_window["from"])
        cutoff = _parse_datetime(telemetry_window["until"])
        start_sql = _clickhouse_datetime(lower_bound)
        cutoff_sql = _clickhouse_datetime(cutoff)

        deduplicated_events_sql = f"""
            SELECT
                event_id,
                argMax(prosthesis_id, loaded_at) AS prosthesis_id
            FROM stg_telemetry
            WHERE event_time >= toDateTime64('{start_sql}', 3, 'UTC')
              AND event_time < toDateTime64('{cutoff_sql}', 3, 'UTC')
            GROUP BY event_id
        """

        client = _clickhouse_client()
        try:
            orphan_events = client.query(
                f"""
                SELECT count()
                FROM ({deduplicated_events_sql}) AS t
                WHERE t.prosthesis_id NOT IN
                (
                    SELECT prosthesis_id
                    FROM stg_user_prosthesis
                    WHERE active = 1
                )
                """
            ).result_rows[0][0]
            expected_events = client.query(
                f"""
                SELECT count()
                FROM ({deduplicated_events_sql}) AS t
                WHERE t.prosthesis_id IN
                (
                    SELECT prosthesis_id
                    FROM stg_user_prosthesis
                    WHERE active = 1
                )
                """
            ).result_rows[0][0]
            actual_events = client.query(
                f"""
                SELECT coalesce(sum(events_count), 0)
                FROM report_by_user_hour
                WHERE period_start >= toDateTime64('{start_sql}', 3, 'UTC')
                  AND period_start < toDateTime64('{cutoff_sql}', 3, 'UTC')
                """
            ).result_rows[0][0]
            duplicate_rows = client.query(
                f"""
                SELECT
                    count()
                    - uniqExact(tuple(user_id, period_start, prosthesis_id))
                FROM report_by_user_hour
                WHERE period_start >= toDateTime64('{start_sql}', 3, 'UTC')
                  AND period_start < toDateTime64('{cutoff_sql}', 3, 'UTC')
                """
            ).result_rows[0][0]
        finally:
            client.close()

        if orphan_events:
            raise ValueError(
                f"Found {orphan_events} telemetry events without an active owner"
            )
        if actual_events != expected_events:
            raise ValueError(
                "Mart event total mismatch: "
                f"expected={expected_events}, actual={actual_events}"
            )
        if duplicate_rows:
            raise ValueError(f"Found {duplicate_rows} duplicate mart keys")

        LOGGER.info(
            "Quality checks passed: events=%s, mart_rows=%s",
            actual_events,
            mart_result["rows"],
        )
        return {
            "events": int(actual_events),
            "mart_rows": mart_result["rows"],
            "orphans": int(orphan_events),
            "duplicates": int(duplicate_rows),
        }

    @task
    def advance_watermark(
        telemetry_window: dict[str, Any],
        quality_result: dict[str, int],
    ) -> str:
        """Publish the processed boundary only after all checks succeed."""
        cutoff = _parse_datetime(telemetry_window["until"])
        run_id = get_current_context()["run_id"]
        now = datetime.now(UTC)

        client = _clickhouse_client()
        try:
            client.insert(
                "etl_state",
                [(PIPELINE_NAME, cutoff, now, run_id)],
                column_names=[
                    "pipeline",
                    "processed_until",
                    "updated_at",
                    "dag_run_id",
                ],
            )
        finally:
            client.close()

        LOGGER.info(
            "Advanced processed_until to %s after validating %s events",
            cutoff.isoformat(),
            quality_result["events"],
        )
        return cutoff.isoformat()

    sources_ready = check_sources()
    crm_result = sync_crm_snapshot(sources_ready)
    telemetry_window = sync_telemetry_increment(sources_ready)
    mart_result = build_report_mart(crm_result, telemetry_window)
    quality_result = validate_report_mart(telemetry_window, mart_result)
    advance_watermark(telemetry_window, quality_result)


reports_etl()
