# BionicPRO Reports ETL

Учебный стенд для второй задачи задания 2. Он моделирует отсутствующие в репозитории CRM и поток телеметрии, запускает почасовой Apache Airflow DAG и формирует пользовательскую витрину в ClickHouse. 

## Состав стенда

| Сервис              | Назначение                                   | Адрес с хоста                                  |
| ------------------- | -------------------------------------------- | ---------------------------------------------- |
| `crm_db`            | PostgreSQL с клиентами и привязками протезов | `localhost:5434`                               |
| `telemetry_db`      | PostgreSQL с событиями протезов              | `localhost:5435`                               |
| `clickhouse`        | Staging, ETL-состояние и отчётная витрина    | HTTP `localhost:8124`, native `localhost:9001` |
| `airflow_db`        | Служебная metadata DB Airflow                | Только внутри Compose                          |
| `airflow_scheduler` | Запуск DAG по расписанию                     | Только внутри Compose                          |
| `airflow_webserver` | Интерфейс Airflow                            | <http://localhost:8081>                        |

Все сервисы приложения, Keycloak и ETL описаны в едином корневом
`docker-compose.yaml`.

## Запуск

Из корня репозитория:

```bash
docker compose up --build -d
```

Проверить состояние:

```bash
docker compose ps
docker compose logs airflow_init
```

Интерфейс Airflow:

- URL: <http://localhost:8081>;
- логин: `admin`;
- пароль: `admin`.

Все пароли в Compose предназначены только для локального учебного стенда.

## Моковые пользователи и данные

CRM использует те же стабильные UUID, логины, email и роли, что и
[`keycloak/realm-export.json`](../keycloak/realm-export.json). Поэтому
`users.user_id` совпадает с claim `sub` access-токена Keycloak.

| Keycloak user | Роль             | Протезы в CRM                | Данные в отчёте |
| ------------- | ---------------- | ---------------------------- | --------------- |
| `user1`       | `user`           | Нет                          | Нет             |
| `user2`       | `user`           | Нет                          | Нет             |
| `admin1`      | `administrator`  | Нет                          | Нет             |
| `prothetic1`  | `prothetic_user` | `BP-HAND-001`, `BP-HAND-004` | Да              |
| `prothetic2`  | `prothetic_user` | `BP-HAND-002`                | Да              |
| `prothetic3`  | `prothetic_user` | `BP-HAND-003`                | Да              |

Для четырёх протезов генерируется история за 30 дней с одним событием каждые
пять минут — всего 34 560 строк телеметрии. Значения движения, времени
реакции и заряда меняются детерминированно, поэтому результат ETL можно
воспроизводить и сравнивать между запусками.

## DAG

Файл DAG: [`dags/reports_etl.py`](./dags/reports_etl.py).

Идентификатор: `reports_etl`.

Расписание:

```text
0 * * * *
```

DAG выполняется в начале каждого часа и публикует только полностью закрытые
часовые интервалы. `catchup=False` не создаёт автоматические исторические
запуски, а `max_active_runs=1` исключает конкурентную публикацию витрины.

Первый запуск читает всю 30-дневную тестовую историю до начала текущего часа. Следующие
запуски читают данные от watermark с перекрытием в два часа. Перекрытие
позволяет подобрать опоздавшие события. Перед повторной вставкой агрегаты
затронутого интервала удаляются синхронно, поэтому повтор DAG не создаёт
дубликаты.

Порядок задач:

```text
check_sources
  ├── sync_crm_snapshot ─┐
  └── sync_telemetry_increment
                         ├── build_report_mart
                         └── validate_report_mart
                               └── advance_watermark
```

`processed_until` обновляется только после проверок:

- у каждого события есть активный владелец;
- число событий в витрине совпадает с числом событий staging;
- ключ `(user_id, period_start, prosthesis_id)` уникален.

## Ручной запуск

DAG не поставлен на паузу при создании. Не дожидаясь следующего часа, его
можно запустить командой:

```bash
docker compose exec airflow_scheduler \
  airflow dags trigger reports_etl
```

Состояние запусков:

```bash
docker compose exec airflow_scheduler \
  airflow dags list-runs --dag-id reports_etl
```

Логи доступны в UI Airflow и через Compose:

```bash
docker compose logs -f airflow_scheduler
```

## Проверка результата

Посмотреть витрину:

```bash
docker compose exec clickhouse \
  clickhouse-client \
  --user bionicpro \
  --password bionicpro_password \
  --database bionicpro \
  --query "
    SELECT
      user_id,
      user_name,
      prosthesis_id,
      period_start,
      events_count,
      round(avg_response_time_ms, 2) AS avg_response_time_ms,
      round(p95_response_time_ms, 2) AS p95_response_time_ms,
      round(avg_battery_percent, 2) AS avg_battery_percent
    FROM report_by_user_hour
    ORDER BY user_id, period_start
    FORMAT PrettyCompact
  "
```

Проверить доступную границу отчётов:

```bash
docker compose exec clickhouse \
  clickhouse-client \
  --user bionicpro \
  --password bionicpro_password \
  --database bionicpro \
  --query "
    SELECT
      pipeline,
      max(processed_until) AS processed_until
    FROM etl_state
    GROUP BY pipeline
  "
```

После повторного запуска DAG количество строк и событий не должно
увеличиваться без добавления новых исходных событий:

```bash
docker compose exec clickhouse \
  clickhouse-client \
  --user bionicpro \
  --password bionicpro_password \
  --database bionicpro \
  --query "
    SELECT
      count() AS rows,
      sum(events_count) AS source_events,
      count() - uniqExact(tuple(user_id, period_start, prosthesis_id))
        AS duplicate_keys
    FROM report_by_user_hour
  "
```

## Добавление тестового события

Событие создаётся в текущем часе, поэтому попадёт в витрину после закрытия
этого часа:

```bash
docker compose exec telemetry_db \
  psql -U telemetry_owner -d telemetry -c "
    INSERT INTO telemetry (
      event_id,
      prosthesis_id,
      event_time,
      movement_type,
      response_time_ms,
      battery_percent
    )
    VALUES (
      gen_random_uuid(),
      'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1',
      CURRENT_TIMESTAMP,
      'hand_open',
      79,
      88
    );
  "
```

Чтобы проверить загрузку сразу, можно записать событие в предыдущий закрытый
час и вручную запустить DAG.

## Структура данных

Исходные таблицы создаются скриптами:

- [`sql/crm/001_schema_and_seed.sql`](./sql/crm/001_schema_and_seed.sql);
- [`sql/telemetry/001_schema_and_seed.sql`](./sql/telemetry/001_schema_and_seed.sql).

ClickHouse-схема находится в
[`sql/clickhouse/001_schema.sql`](./sql/clickhouse/001_schema.sql).

Витрина `report_by_user_hour` содержит одну строку на пользователя, протез и
час. Она партиционирована по месяцу и отсортирована по:

```text
(user_id, period_start, prosthesis_id)
```

Такой ключ соответствует будущему запросу API: фильтр по
аутентифицированному пользователю и временному диапазону.

## Остановка и очистка

Остановить контейнеры, сохранив данные:

```bash
docker compose down
```

Удалить контейнеры вместе с тестовыми базами и выполнить чистый запуск:

```bash
docker compose down -v
```
