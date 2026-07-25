# BionicPRO Reports API

Сервис читает подготовленную Airflow витрину `report_by_user_hour` из ClickHouse и не выполняет объединение CRM, телеметрии либо тяжёлые агрегации во время пользовательского запроса.

## Запуск

Сервис входит в единый корневой Compose:

```bash
docker compose up --build -d
```

Адрес: <http://localhost:8000>.

Проверки состояния:

```bash
curl http://localhost:8000/healthz
curl http://localhost:8000/readyz
```

## API

```http
GET /reports?user_id=<uuid>&from=<RFC3339>&to=<RFC3339>
Authorization: Bearer <Keycloak access token>
```

Все query-параметры необязательны:

- `user_id` по умолчанию равен JWT `sub`;
- `from` по умолчанию равен `processed_until - 30 дней`;
- `to` по умолчанию равен `processed_until`.

Пример ответа:

```json
{
  "user_id": "44a50ba4-d96c-4c5c-b0f6-602069c63aa6",
  "from": "2026-06-21T14:00:00Z",
  "to": "2026-07-21T14:00:00Z",
  "processed_until": "2026-07-21T14:00:00Z",
  "items": [
    {
      "prosthesis_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1",
      "serial_number": "BP-HAND-001",
      "period_start": "2026-07-21T13:00:00Z",
      "events_count": 12,
      "avg_response_time_ms": 83.5,
      "p95_response_time_ms": 101,
      "avg_battery_percent": 64.5,
      "last_event_at": "2026-07-21T13:55:10Z"
    }
  ]
}
```

Если `to` находится после `processed_until`, сервис возвращает `422` и доступную границу данных.

## Ограничение доступа

Сервис принимает только JWT:

- подписанный ключом Keycloak (`RS256`, ключ получается через JWKS);
- с issuer `reports-realm`;
- с audience `reports-api`;
- с неистёкшим сроком действия;
- с realm-ролью `prothetic_user`.

Идентификатор для ClickHouse-запроса всегда берётся из проверенного JWT `sub`. Если клиент передал другой `user_id`, сервис возвращает `403` до обращения к ClickHouse. У сервиса есть отдельная ClickHouse-учётная запись с правом `SELECT` только для `report_by_user_hour` и `etl_state`.

Основные статусы:

| Статус | Условие                                                             |
| ------ | ------------------------------------------------------------------- |
| `200`  | Собственный отчёт успешно получен                                   |
| `401`  | Токен отсутствует, некорректен, просрочен или имеет другую audience |
| `403`  | Нет роли `prothetic_user` либо запрошен чужой `user_id`             |
| `422`  | Запрошенный период ещё не обработан Airflow                         |
| `503`  | Нет опубликованного ETL-интервала                                   |

## Тесты

```bash
cd reports-api
go test ./...
```

Тесты проверяют успешный собственный запрос, запрет чужого отчёта до вызова OLAP, роль, audience и границу обработанного периода.
