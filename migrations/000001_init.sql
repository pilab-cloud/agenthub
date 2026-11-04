-- +goose Up
-- AgentHub initial schema. PostgreSQL, one schema (agenthub), goose migrations.
CREATE SCHEMA IF NOT EXISTS agenthub;

-- Hub identity and federation
CREATE TABLE agenthub.hubs (
  id         text PRIMARY KEY,
  base_url   text NOT NULL,
  public_key text,
  protocol   int  NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE agenthub.upstreams (
  id            uuid PRIMARY KEY,
  local_hub_id  text NOT NULL REFERENCES agenthub.hubs(id),
  remote_hub_id text NOT NULL,
  base_url      text NOT NULL,
  mode          text NOT NULL,
  trust_policy  jsonb NOT NULL,
  sync_cursor   text,
  last_sync_at  timestamptz,
  status        text NOT NULL DEFAULT 'active',
  UNIQUE (local_hub_id, remote_hub_id)
);

-- Publishing
CREATE TABLE agenthub.namespaces (
  id          uuid PRIMARY KEY,
  hub_id      text NOT NULL REFERENCES agenthub.hubs(id),
  slug        text NOT NULL,
  display_name text NOT NULL,
  description text,
  visibility  text NOT NULL DEFAULT 'public',
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (hub_id, slug)
);

CREATE TABLE agenthub.assets (
  id             uuid PRIMARY KEY,
  hub_id         text NOT NULL REFERENCES agenthub.hubs(id),
  namespace_id   uuid NOT NULL REFERENCES agenthub.namespaces(id),
  kind           text NOT NULL,
  name           text NOT NULL,
  display_name   text,
  summary        text NOT NULL,
  description    text,
  license        text,
  homepage       text,
  repository     text,
  icon_digest    text,
  tags           text[] NOT NULL DEFAULT '{}',
  categories     text[] NOT NULL DEFAULT '{}',
  latest_version text,
  landing_visible boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (namespace_id, kind, name)
);

CREATE TABLE agenthub.asset_versions (
  id                uuid PRIMARY KEY,
  asset_id          uuid NOT NULL REFERENCES agenthub.assets(id),
  version           text NOT NULL,
  status            text NOT NULL DEFAULT 'draft',
  manifest          jsonb NOT NULL,
  manifest_digest   text NOT NULL,
  bundle_digest     text NOT NULL,
  origin_hub_id     text,
  origin_version_id uuid,
  published_by      text,
  published_at      timestamptz,
  yanked_at         timestamptz,
  deprecation_note  text,
  readme            text,
  changelog         text,
  UNIQUE (asset_id, version),
  UNIQUE (manifest_digest)
);

CREATE TABLE agenthub.asset_files (
  version_id uuid NOT NULL REFERENCES agenthub.asset_versions(id) ON DELETE CASCADE,
  path       text NOT NULL,
  media_type text NOT NULL,
  size       bigint NOT NULL,
  sha256     text NOT NULL,
  mode       int NOT NULL DEFAULT 420,
  executable boolean NOT NULL DEFAULT false,
  PRIMARY KEY (version_id, path)
);

CREATE TABLE agenthub.dependencies (
  version_id  uuid NOT NULL REFERENCES agenthub.asset_versions(id) ON DELETE CASCADE,
  kind        text NOT NULL,
  target_hub  text,
  target_ns   text NOT NULL,
  target_kind text NOT NULL,
  target_name text NOT NULL,
  constraint  text NOT NULL,
  PRIMARY KEY (version_id, kind, target_ns, target_kind, target_name)
);

CREATE TABLE agenthub.install_guides (
  version_id uuid NOT NULL REFERENCES agenthub.asset_versions(id) ON DELETE CASCADE,
  harness    text NOT NULL,
  scope      text NOT NULL,
  path       text NOT NULL,
  body       text NOT NULL,
  PRIMARY KEY (version_id, harness, scope)
);

CREATE TABLE agenthub.signing_keys (
  id          uuid PRIMARY KEY,
  namespace_id uuid NOT NULL REFERENCES agenthub.namespaces(id),
  kid         text NOT NULL,
  algorithm   text NOT NULL,
  custody     text NOT NULL DEFAULT 'client',
  public_key  text NOT NULL,
  kms_key_ref text,
  created_by  text,
  valid_from  timestamptz NOT NULL,
  revoked_at  timestamptz,
  UNIQUE (namespace_id, kid)
);

CREATE TABLE agenthub.asset_drafts (
  id         uuid PRIMARY KEY,
  namespace_id uuid NOT NULL REFERENCES agenthub.namespaces(id),
  kind       text NOT NULL,
  name       text NOT NULL,
  author_sub text NOT NULL,
  manifest   jsonb NOT NULL,
  files      jsonb NOT NULL,
  status     text NOT NULL DEFAULT 'draft',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (namespace_id, kind, name, author_sub)
);

CREATE TABLE agenthub.catalog_state (
  hub_id     text PRIMARY KEY REFERENCES agenthub.hubs(id),
  revision   bigint NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE agenthub.audit_log (
  id        bigserial PRIMARY KEY,
  at        timestamptz NOT NULL DEFAULT now(),
  actor_sub text,
  actor_email text,
  action    text NOT NULL,
  namespace text,
  asset     text,
  version   text,
  detail    jsonb,
  ip        inet
);

CREATE TABLE agenthub.download_counters (
  asset_id   uuid NOT NULL REFERENCES agenthub.assets(id),
  version_id uuid NOT NULL REFERENCES agenthub.asset_versions(id),
  day        date NOT NULL,
  count      bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (asset_id, version_id, day)
);

-- Indexes
CREATE INDEX idx_assets_search ON agenthub.assets USING gin (to_tsvector('simple', coalesce(display_name,'') || ' ' || coalesce(summary,'') || ' ' || coalesce(description,'')));
CREATE INDEX idx_assets_tags ON agenthub.assets USING gin (tags);
CREATE INDEX idx_assets_categories ON agenthub.assets USING gin (categories);
CREATE INDEX idx_versions_status ON agenthub.asset_versions (status, published_at DESC);
CREATE INDEX idx_versions_landing ON agenthub.asset_versions (published_at DESC) WHERE status = 'published';
CREATE INDEX idx_versions_origin ON agenthub.asset_versions (origin_hub_id, origin_version_id);
CREATE INDEX idx_assets_namespace_updated ON agenthub.assets (namespace_id, updated_at DESC);
CREATE INDEX idx_assets_landing ON agenthub.assets (namespace_id, updated_at DESC) WHERE landing_visible;
CREATE INDEX idx_drafts_author ON agenthub.asset_drafts (namespace_id, author_sub, updated_at DESC);
CREATE INDEX idx_audit_at ON agenthub.audit_log (at DESC);

-- +goose Down
DROP SCHEMA IF EXISTS agenthub CASCADE;
