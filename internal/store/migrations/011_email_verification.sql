-- Подтверждение почты кодом: пока email не подтверждён, аккаунт только смотрит.
-- Те, кто зарегистрировался раньше, считаются подтверждёнными.
ALTER TABLE users ADD COLUMN email_verified_at timestamptz;
UPDATE users SET email_verified_at = created_at;

-- Один действующий код на пользователя; в базе только SHA-256 (вместе с id).
CREATE TABLE email_codes (
    user_id    bigint      PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    code_hash  bytea       NOT NULL,
    expires_at timestamptz NOT NULL,
    sent_at    timestamptz NOT NULL,
    attempts   int         NOT NULL DEFAULT 0
);
