-- Уведомления в Telegram: привязка аккаунта к чату и очередь отправки (outbox).
ALTER TABLE users ADD COLUMN telegram_chat_id bigint UNIQUE;

-- Одноразовые коды привязки из ссылки t.me/<бот>?start=<код>. В базе только SHA-256 кода.
CREATE TABLE telegram_links (
    code_hash  bytea       PRIMARY KEY,
    user_id    bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);
CREATE INDEX telegram_links_user_idx ON telegram_links (user_id);

-- Outbox: строка пишется в той же транзакции, что и событие, отправляет воркер.
-- dedup_key не даёт поставить одно и то же напоминание дважды.
CREATE TABLE notifications (
    id              bigserial   PRIMARY KEY,
    user_id         bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind            text        NOT NULL,
    text            text        NOT NULL,
    dedup_key       text        UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    attempts        integer     NOT NULL DEFAULT 0,
    sent_at         timestamptz,
    failed_at       timestamptz, -- бросили: бот заблокирован или кончились попытки
    last_error      text        NOT NULL DEFAULT ''
);
CREATE INDEX notifications_due_idx ON notifications (next_attempt_at) WHERE sent_at IS NULL AND failed_at IS NULL;
