-- +goose Up
-- M3.6: persisted namespace RBAC bindings. One row per
-- (hub, namespace, subject); `role` holds an internal/authz role
-- (reader | publisher | maintainer | owner), validated in Go on write so
-- the vocabulary stays in one place.
CREATE TABLE agenthub.namespace_roles (
  hub_id     text        NOT NULL,
  namespace  text        NOT NULL,
  subject    text        NOT NULL,
  role       text        NOT NULL,
  granted_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (hub_id, namespace, subject)
);

CREATE INDEX idx_namespace_roles_ns ON agenthub.namespace_roles (hub_id, namespace);

-- +goose Down
DROP TABLE IF EXISTS agenthub.namespace_roles;
