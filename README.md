# BionicPRO

## Навигация

### Задание 1. Управление учётными данными

- [Описание архитектурного решения и ADR](./task1_docs/task_1_adr.md)
- [C4-диаграмма](./task1_docs/BionicPRO_C4_model.puml)

### Задание 2. Сервис отчётов

- [Описание архитектурного решения, реализации и проверки](./task2_docs/task_2_adr.md)
- [C4-диаграмма подготовки и получения отчётов](./task2_docs/BionicPRO_C4_model.puml)

Реализация второго задания:

| Часть решения                                        | Расположение                                                                         |
| ---------------------------------------------------- | ------------------------------------------------------------------------------------ |
| Единый Docker Compose                                | [`docker-compose.yaml`](./docker-compose.yaml)                                       |
| Airflow DAG                                          | [`airflow/dags/reports_etl.py`](./airflow/dags/reports_etl.py)                       |
| Схемы и тестовые данные CRM, телеметрии и ClickHouse | [`airflow/sql`](./airflow/sql)                                                       |
| Инструкция и описание ETL                            | [`airflow/README.md`](./airflow/README.md)                                           |
| Reports API на Go                                    | [`reports-api`](./reports-api)                                                       |
| Описание Reports API                                 | [`reports-api/README.md`](./reports-api/README.md)                                   |
| UI получения CSV-отчёта                              | [`frontend/src/components/ReportPage.tsx`](./frontend/src/components/ReportPage.tsx) |
| Формирование CSV                                     | [`frontend/src/report.ts`](./frontend/src/report.ts)                                 |
| Realm, тестовые пользователи и роли Keycloak         | [`keycloak/realm-export.json`](./keycloak/realm-export.json)                         |

## Быстрый запуск задания 2

Из корня репозитория:

```bash
docker compose up --build -d
docker compose ps
```

После запуска доступны:

| Сервис | Адрес | Учётные данные |
| --- | --- | --- |
| BionicPRO UI | <http://localhost:3000> | `prothetic1 / prothetic123` |
| Airflow | <http://localhost:8081> | `admin / admin` |
| Keycloak | <http://localhost:8080> | `admin / admin` для консоли администратора |
| Reports API | <http://localhost:8000> | Доступ через JWT пользователя |

Полный сценарий проверки и обоснование решений приведены в [ADR второго задания](./task2_docs/task_2_adr.md).
