-- Места от пользователей: «Предложить место» → черновик (draft) с автором →
-- модерация в админке: опубликовать или отклонить с причиной.
ALTER TABLE places
    ADD COLUMN author_id     bigint REFERENCES users (id) ON DELETE SET NULL,
    ADD COLUMN reject_reason text NOT NULL DEFAULT '',
    ADD COLUMN reviewed_at   timestamptz;

ALTER TABLE places DROP CONSTRAINT places_status_check;
ALTER TABLE places ADD CONSTRAINT places_status_check
    CHECK (status IN ('draft', 'published', 'hidden', 'rejected'));

CREATE INDEX places_author_idx ON places (author_id, created_at DESC) WHERE author_id IS NOT NULL;
-- очередь модерации
CREATE INDEX places_draft_idx ON places (created_at) WHERE status = 'draft' AND author_id IS NOT NULL;
