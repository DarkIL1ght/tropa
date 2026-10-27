# Tropa

Платформа для организации походов: организатор создаёт поход, а Tropa сама ведёт запись и лист ожидания, рассаживает участников по машинам и палаткам, следит за взносами и снаряжением и напоминает в Telegram.

Стек: Go (стандартный `net/http`, `html/template`), PostgreSQL (`pgx`), без сборки фронтенда.

## Запуск

С Docker:

```sh
docker compose up --build
# http://127.0.0.1:8080
```

С локальным PostgreSQL:

```sh
psql -U postgres -c "CREATE ROLE tropa LOGIN PASSWORD 'tropa'" -c "CREATE DATABASE tropa OWNER tropa"
export DATABASE_URL=postgres://tropa:tropa@localhost:5432/tropa?sslmode=disable
go run ./cmd/tropa serve
```

## Тесты

```sh
go test ./...                     # без базы тесты, которым нужна БД, пропускаются
TROPA_TEST_DATABASE_URL=$DATABASE_URL go test ./...
```

Каждый тест с базой работает в своей временной схеме, поэтому тесты не мешают друг другу и не трогают рабочие данные.

## Статус

- [x] Аккаунты: регистрация, вход, сессии в БД, CSRF
- [ ] Походы и каталог
- [ ] Запись и лист ожидания с автопродвижением очереди
- [ ] Уведомления в Telegram (outbox + воркер)
- [ ] Автораспределение по машинам и палаткам
- [ ] Взносы и снаряжение
