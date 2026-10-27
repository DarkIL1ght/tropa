# Контекст для Claude Code: Tropa

## Что это
Пет-проект Дархана для портфолио (Go-бэкенд): карта мест Алматы и Алматинской области, куда сходить с друзьями (природа, активный отдых, места в тепле, культура), и встречи, которые собираются через карту. Не коммерческий продукт. Только Алматы и область. Полный план этапов: `~/.claude/plans/harmonic-drifting-kernighan.md`.

## Стек
- Go 1.25, `net/http` ServeMux с паттернами методов, `html/template`, без JS-сборки. Шаблоны, статика и SQL-миграции встроены через `go:embed`.
- PostgreSQL через `pgx/v5` (пул `pgxpool`). Внешние зависимости — только `pgx` и `x/crypto`; Telegram — без SDK, на `net/http`.
- Интерфейс и комментарии в коде — на русском.

## Карта кода
- `cmd/tropa/main.go` — команды `serve` (миграции при старте, фоновая очистка сессий), `migrate`, `seed [папка]`, `make-admin <email>`; настройки — переменные окружения (см. шапку файла).
- `internal/store` — `Open`, `Migrate` (файлы `migrations/NNN_*.sql` по порядку, каждый в своей транзакции, под advisory lock). Новая миграция — новый файл, старые не менять.
- `internal/store/storetest` — `storetest.New(t)`: пул в новой схеме со всеми миграциями; без `TROPA_TEST_DATABASE_URL` тест пропускается.
- `internal/auth` — пользователи (bcrypt, email без учёта регистра), сессии (в БД только SHA-256 токена), `Middleware` (пользователь в контексте + CSRF double-submit для всех не-GET), `RequireUser`, `SafeNext` для редиректов.
- `internal/places` — места. `Place` (теги json = формат `data/places/*.json`), группы `Category` и виды `Kind` (`kinds`), рамки `RegionBBox`/`CityBBox`, `Normalize` + `Validate`, `TwoGISURL` (обычная ссылка на 2gis.kz). `store.go`: `Upsert` по slug (не трогает строку, если поля те же — `IS DISTINCT FROM`), `Get` (только published), `Search(Query)`. `seed.go`: `LoadSeed` (все файлы, кроме `candidates*`, `DisallowUnknownFields`, уникальность slug и osm_ref; при ошибке — ничего), `Seed` одной транзакцией.
- `internal/trips` — походы (по плану v2 станут встречами `internal/meetups`, привязанными к месту). `Input.Validate` (форма строками → поля + ошибки по полям), статусы и ручные переходы (`transitions`; `full` ставит только запись), `Update`/`SetStatus` в транзакции под `FOR UPDATE` с проверкой прав (организатор или админ); `SetStatus` принимает статус, который видел пользователь, и при расхождении отдаёт `ErrStaleStatus`. `catalog.go` — `Filter` (из query и обратно), `Catalog` (только open/full/closed, предстоящие или прошедшие, LIKE с экранированием), `Regions`, `ByOrganizer`. «Сегодня» — по Алматы (`Service.Today`, `Now` подменяется в тестах). Черновики и отменённые чужим отдаются как 404.
- `internal/web` — `Server`, маршруты в `Handler()`, `render` (layout + страница, рендер в буфер), flash-сообщения по коду (`flashText`). Каждая страница — `templates/pages/*.html` с блоками `title` и `content`; общие куски (`trip_card`) — в `layout.html`. Форматирование для шаблонов (`dateRange`, `money`, `plural`) — `format.go`. В формах обязательно скрытое поле `csrf_token`.

## Данные мест
- Источник — OpenStreetMap (ODbL, подпись «© участники OpenStreetMap») и ручное курирование. **Данные 2ГИС не брать** ни API, ни парсингом: лицензия запрещает хранение (law.2gis.ru/api-rules, п. 4.2). 2ГИС — только ссылкой.
- Координаты не писать по памяти: проверять по OSM (Nominatim/Overpass, ≤ 1 запроса в секунду, User-Agent со ссылкой на репозиторий, без личных email) и указывать `osm_ref`.
- Новые места — в `data/places/*.json`, затем `go run ./cmd/tropa seed`. `TestRepoSeedFilesValid` проверяет настоящие файлы.

## Правила
- Коммиты без строк Co-Authored-By и «Generated with Claude Code»: автор только Дархан.
- Логика записи и листа ожидания — в транзакциях с блокировкой строки похода; проверять конкурентным тестом на реальной базе.
- Перед коммитом: `gofmt -l .`, `go vet ./...`, `go test ./...` (с тестовой базой).
