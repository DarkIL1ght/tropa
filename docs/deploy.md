# Деплой на VPS

Схема: на одной виртуалке в Docker Compose работают Caddy (HTTPS, сертификат Let's Encrypt), Tropa и PostgreSQL 18. Наружу открыты только порты 80 и 443, база и приложение — во внутренней сети compose. Бот уведомлений работает через long polling, поэтому вебхук и отдельный адрес для него не нужны.

Файлы: `compose.prod.yml`, `deploy/Caddyfile`, `deploy/.env.prod.example`, `deploy/backup.sh`.

## 1. Сервер

Подойдёт любой VPS с Ubuntu 24.04, от 1 ГБ памяти и 10 ГБ диска:
- **Oracle Cloud Always Free** — бесплатно. Лучше машина Ampere A1 (ARM, до 4 ядер и 24 ГБ); образы `golang`, `postgres`, `caddy` и `distroless` собраны и под ARM. При регистрации нужна карта для проверки. Если A1 «нет в наличии» — попробовать позже или взять VM.Standard.E2.1.Micro (1 ГБ, тогда обязательно swap, см. ниже).
- Любой платный VPS за 4–5 $ в месяц — то же самое без ограничений Oracle.

При создании добавить свой SSH-ключ (`ssh-keygen -t ed25519`, публичная часть — `~/.ssh/id_ed25519.pub`).

**Oracle:** в настройках подсети (Virtual Cloud Network → Security List) добавить входящие правила для TCP 80 и 443 с `0.0.0.0/0`. Кроме того, в образах Oracle Ubuntu закрыты порты в iptables — шаг ниже.

## 2. Подготовка (на сервере)

```sh
ssh ubuntu@<IP>
sudo apt update && sudo apt upgrade -y

# Docker с плагином compose
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker $USER   # затем выйти и зайти по ssh заново

# Oracle Ubuntu: в образе свои правила iptables (в конце INPUT — REJECT), ufw не включать.
# Открыть 80/443 правилами в начале цепочки и сохранить:
sudo iptables -I INPUT -p tcp -m multiport --dports 80,443 -j ACCEPT
sudo iptables -I INPUT -p udp --dport 443 -j ACCEPT
sudo netfilter-persistent save
# Другой VPS: ufw
# sudo ufw allow OpenSSH && sudo ufw allow 80,443/tcp && sudo ufw allow 443/udp && sudo ufw enable

sudo timedatectl set-timezone Asia/Almaty   # время в логах и бэкапах

# Только если памяти 1 ГБ: swap 2 ГБ, иначе сборка Go может упасть
sudo fallocate -l 2G /swapfile && sudo chmod 600 /swapfile && sudo mkswap /swapfile && sudo swapon /swapfile
echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
```

## 3. Домен

A-запись домена (`tropa.<домен>`) — на IP сервера. Бесплатно: поддомен DuckDNS (`<имя>.duckdns.org`). Caddy получит сертификат сам, как только домен начнёт указывать на сервер и будут открыты 80/443.

## 4. Запуск

```sh
git clone https://github.com/DarkIL1ght/Invication.git tropa && cd tropa
cp deploy/.env.prod.example .env.prod
nano .env.prod   # DOMAIN, POSTGRES_PASSWORD (openssl rand -hex 24), TELEGRAM_BOT_TOKEN

docker compose -f compose.prod.yml --env-file .env.prod up -d --build
docker compose -f compose.prod.yml --env-file .env.prod logs -f app caddy   # Ctrl+C — выйти
```

Миграции применяются при старте `serve`. Места и админ:

```sh
docker compose -f compose.prod.yml --env-file .env.prod run --rm app seed /data/places
# сначала зарегистрироваться на сайте, затем:
docker compose -f compose.prod.yml --env-file .env.prod run --rm app make-admin <email>
# пользователь без Telegram забыл пароль — ссылка сброса на сутки, передать ему:
docker compose -f compose.prod.yml --env-file .env.prod run --rm app reset-link <email>
```

Чтобы не писать длинную команду: `alias dc='docker compose -f compose.prod.yml --env-file .env.prod'` в `~/.bashrc`.

## 5. Telegram

Для продакшна — **отдельный** бот в @BotFather (не тот, что для разработки): long polling с одним токеном можно запускать только в одном экземпляре, иначе Telegram отдаёт 409. Настройка бота — `docs/telegram.md`. Ссылки в сообщениях строятся от `SITE_URL` = `https://DOMAIN` (задаётся в `compose.prod.yml`).

## 6. Обновление

```sh
cd ~/tropa && git pull
docker compose -f compose.prod.yml --env-file .env.prod up -d --build
# если менялись data/places:
docker compose -f compose.prod.yml --env-file .env.prod run --rm app seed /data/places
docker image prune -f
```

## 7. Резервные копии

```sh
chmod +x deploy/backup.sh
crontab -e
# каждый день в 04:00 по времени сервера:
0 4 * * * $HOME/tropa/deploy/backup.sh
```

Восстановление: `docker compose -f compose.prod.yml --env-file .env.prod exec -T db pg_restore -U tropa -d tropa --clean < ~/tropa-backups/tropa-<дата>.dump`. Копии на том же сервере не спасут от потери машины — время от времени скачивать их к себе (`scp`).

## Проверка после запуска

- `https://DOMAIN` открывается, замок в браузере, `http://` перенаправляет на `https://`.
- Регистрация и вход работают (cookie с `Secure`).
- Карта показывает места после `seed`.
- `/me/settings` → привязка Telegram → бот присылает подтверждение.
