-- Встречи на месте вместо походов. Реальных походов нет — таблицу удаляем.
DROP TABLE trips;

CREATE TABLE meetups (
    id           bigserial PRIMARY KEY,
    -- ссылка /m/{token}; для встреч «для друзей» — единственный способ их открыть
    token        text        NOT NULL UNIQUE,
    place_id     bigint      NOT NULL REFERENCES places (id),
    organizer_id bigint      NOT NULL REFERENCES users (id),
    title        text        NOT NULL,
    description  text        NOT NULL DEFAULT '',
    starts_at    timestamptz NOT NULL,
    capacity     integer     CHECK (capacity BETWEEN 2 AND 200), -- NULL — без лимита
    visibility   text        NOT NULL DEFAULT 'public' CHECK (visibility IN ('public', 'friends')),
    status       text        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed', 'cancelled')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX meetups_organizer_idx ON meetups (organizer_id);
-- список и карта: открытые публичные встречи по времени
CREATE INDEX meetups_public_idx ON meetups (starts_at) WHERE visibility = 'public' AND status <> 'cancelled';
CREATE INDEX meetups_place_idx ON meetups (place_id, starts_at);

-- Участники. confirmed — идёт; offered — освободилось место, ждём ответа
-- до offer_expires_at (место держится за ним); waitlist — очередь, position с 1.
CREATE TABLE participants (
    meetup_id        bigint      NOT NULL REFERENCES meetups (id) ON DELETE CASCADE,
    user_id          bigint      NOT NULL REFERENCES users (id),
    status           text        NOT NULL CHECK (status IN ('confirmed', 'waitlist', 'offered')),
    position         integer,
    offer_expires_at timestamptz,
    joined_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (meetup_id, user_id),
    CHECK ((status = 'waitlist') = (position IS NOT NULL)),
    CHECK ((status = 'offered') = (offer_expires_at IS NOT NULL))
);
CREATE INDEX participants_user_idx ON participants (user_id);
CREATE INDEX participants_offers_idx ON participants (offer_expires_at) WHERE status = 'offered';
-- позиции в очереди уникальны; проверка в конце транзакции, чтобы сдвигать очередь одним UPDATE
ALTER TABLE participants ADD CONSTRAINT participants_position_key
    UNIQUE (meetup_id, position) DEFERRABLE INITIALLY DEFERRED;
