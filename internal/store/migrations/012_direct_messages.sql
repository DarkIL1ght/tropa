-- Личные сообщения между друзьями.
CREATE TABLE direct_messages (
    id           bigserial   PRIMARY KEY,
    sender_id    bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    recipient_id bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    body         text        NOT NULL CHECK (char_length(body) BETWEEN 1 AND 1000),
    created_at   timestamptz NOT NULL DEFAULT now(),
    read_at      timestamptz,
    CHECK (sender_id <> recipient_id)
);
-- переписка пары по порядку (в любую сторону) и догрузка «после id»
CREATE INDEX direct_messages_pair_idx ON direct_messages (least(sender_id, recipient_id), greatest(sender_id, recipient_id), id);
-- непрочитанные для счётчика в шапке
CREATE INDEX direct_messages_unread_idx ON direct_messages (recipient_id) WHERE read_at IS NULL;
