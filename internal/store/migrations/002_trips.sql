-- Походы. Взнос — в целых тенге.
CREATE TABLE trips (
    id            bigserial PRIMARY KEY,
    organizer_id  bigint      NOT NULL REFERENCES users (id),
    title         text        NOT NULL,
    description   text        NOT NULL DEFAULT '',
    region        text        NOT NULL,
    meeting_point text        NOT NULL DEFAULT '',
    start_date    date        NOT NULL,
    end_date      date        NOT NULL,
    difficulty    text        NOT NULL CHECK (difficulty IN ('easy', 'medium', 'hard')),
    capacity      integer     NOT NULL CHECK (capacity BETWEEN 1 AND 500),
    fee           integer     NOT NULL DEFAULT 0 CHECK (fee >= 0),
    status        text        NOT NULL DEFAULT 'draft'
                  CHECK (status IN ('draft', 'open', 'full', 'closed', 'done', 'cancelled')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK (end_date >= start_date)
);
CREATE INDEX trips_organizer_idx ON trips (organizer_id);
-- каталог: публичные походы по дате начала
CREATE INDEX trips_catalog_idx ON trips (start_date) WHERE status IN ('open', 'full', 'closed');
