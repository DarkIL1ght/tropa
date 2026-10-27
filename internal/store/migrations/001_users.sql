-- Пользователи и сессии.
CREATE TABLE users (
    id            bigserial PRIMARY KEY,
    email         text        NOT NULL,
    name          text        NOT NULL,
    password_hash text        NOT NULL,
    role          text        NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    created_at    timestamptz NOT NULL DEFAULT now()
);
-- email сравниваем без учёта регистра
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

-- В базе только SHA-256 токена: утечка таблицы не даёт войти чужой сессией.
CREATE TABLE sessions (
    token_hash bytea       PRIMARY KEY,
    user_id    bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);
