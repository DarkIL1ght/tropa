#!/bin/sh
# Ежедневная копия базы: deploy/backup.sh из корня репозитория (cron — см. docs/deploy.md).
# Копии лежат в ~/tropa-backups, хранятся 14 дней.
set -eu
cd "$(dirname "$0")/.."
dir="$HOME/tropa-backups"
mkdir -p "$dir"
docker compose -f compose.prod.yml --env-file .env.prod exec -T db \
	pg_dump -U tropa --format=custom tropa >"$dir/tropa-$(date +%F).dump"
find "$dir" -name 'tropa-*.dump' -mtime +14 -delete
