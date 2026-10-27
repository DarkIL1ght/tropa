# Контекст для Claude Code: Tropa

## Что это
Пет-проект Дархана для портфолио (Go-бэкенд): платформа для организации походов с автоматическим управлением участниками. Не коммерческий продукт. Полный план этапов: `~/.claude/plans/harmonic-drifting-kernighan.md`.

## Стек
- Go 1.25, `net/http` ServeMux с паттернами методов, `html/template`, без JS-сборки. Шаблоны, статика и SQL-миграции встроены через `go:embed`.
- PostgreSQL через `pgx/v5` (пул `pgxpool`). Внешние зависимости — только `pgx` и `x/crypto`; Telegram — без SDK, на `net/http`.
- Интерфейс и комментарии в коде — на русском.

## Карта кода
- `cmd/tropa/main.go` — команды `serve` (миграции при старте, фоновая очистка сессий) и `migrate`; настройки — переменные окружения (см. шапку файла).
- `internal/store` — `Open`, `Migrate` (файлы `migrations/NNN_*.sql` по порядку, каждый в своей транзакции, под advisory lock). Новая миграция — новый файл, старые не менять.
- `internal/store/storetest` — `storetest.New(t)`: пул в новой схеме со всеми миграциями; без `TROPA_TEST_DATABASE_URL` тест пропускается.
- `internal/auth` — пользователи (bcrypt, email без учёта регистра), сессии (в БД только SHA-256 токена), `Middleware` (пользователь в контексте + CSRF double-submit для всех не-GET), `RequireUser`, `SafeNext` для редиректов.
- `internal/web` — `Server`, маршруты в `Handler()`, `render` (layout + страница, рендер в буфер), flash-сообщения по коду (`flashText`). Каждая страница — `templates/pages/*.html` с блоками `title` и `content`. В формах обязательно скрытое поле `csrf_token`.

## Правила
- Коммиты без строк Co-Authored-By и «Generated with Claude Code»: автор только Дархан.
- Логика записи и листа ожидания — в транзакциях с блокировкой строки похода; проверять конкурентным тестом на реальной базе.
- Перед коммитом: `gofmt -l .`, `go vet ./...`, `go test ./...` (с тестовой базой).
