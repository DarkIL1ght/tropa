-- Места на карте: Алматы и Алматинская область.
CREATE TABLE places (
    id            bigserial PRIMARY KEY,
    slug          text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    name          text        NOT NULL,
    category      text        NOT NULL CHECK (category IN ('nature', 'active', 'indoor', 'culture')),
    kind          text        NOT NULL,
    lat           double precision NOT NULL,
    lon           double precision NOT NULL,
    area          text        NOT NULL CHECK (area IN ('almaty', 'region')),
    address       text        NOT NULL DEFAULT '',
    description   text        NOT NULL DEFAULT '',
    indoor        boolean     NOT NULL DEFAULT false,
    free          boolean     NOT NULL DEFAULT false,
    season        text        NOT NULL DEFAULT 'all_year' CHECK (season IN ('all_year', 'summer', 'winter')),
    website       text        NOT NULL DEFAULT '',
    opening_hours text        NOT NULL DEFAULT '', -- в формате OSM, как есть
    source        text        NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'osm')),
    osm_ref       text        UNIQUE,              -- "node/123", "way/456"
    status        text        NOT NULL DEFAULT 'published' CHECK (status IN ('draft', 'published', 'hidden')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
-- карта запрашивает видимую рамку; для сотен–тысяч мест хватает btree
CREATE INDEX places_published_geo_idx ON places (lat, lon) WHERE status = 'published';
