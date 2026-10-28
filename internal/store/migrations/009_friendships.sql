-- Друзья: взаимно, через заявку. Одна строка на пару: user_id отправил заявку friend_id.
CREATE TABLE friendships (
    user_id     bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    friend_id   bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status      text        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    accepted_at timestamptz,
    PRIMARY KEY (user_id, friend_id),
    CHECK (user_id <> friend_id)
);
-- пара уникальна в любую сторону: встречные заявки не создают вторую строку
CREATE UNIQUE INDEX friendships_pair_key ON friendships (least(user_id, friend_id), greatest(user_id, friend_id));
CREATE INDEX friendships_friend_id_idx ON friendships (friend_id);
