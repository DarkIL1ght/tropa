-- Чат встречи: пишут и читают участники (идут или им предложено место).
CREATE TABLE messages (
    id         bigserial   PRIMARY KEY,
    meetup_id  bigint      NOT NULL REFERENCES meetups (id) ON DELETE CASCADE,
    user_id    bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    body       text        NOT NULL CHECK (char_length(body) BETWEEN 1 AND 1000),
    created_at timestamptz NOT NULL DEFAULT now()
);
-- история встречи и догрузка «после id» для потока SSE
CREATE INDEX messages_meetup_id_idx ON messages (meetup_id, id);
