-- Migration: 0001_init
CREATE TABLE IF NOT EXISTS inboxes (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  token TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL CHECK (status IN ('active', 'disabled')),
  description TEXT NULL,
  retention_days INTEGER NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  disabled_at TEXT NULL,
  deleted_at TEXT NULL
);

CREATE TABLE IF NOT EXISTS captured_requests (
  id TEXT PRIMARY KEY,
  inbox_id TEXT NOT NULL,
  method TEXT NOT NULL,
  path TEXT NOT NULL,
  query_params_json TEXT NOT NULL,
  headers_json TEXT NOT NULL,
  body_raw BLOB NOT NULL,
  body_size_bytes INTEGER NOT NULL,
  content_type TEXT NOT NULL,
  remote_ip TEXT NOT NULL,
  user_agent TEXT NOT NULL,
  received_at TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  truncated_body INTEGER NOT NULL DEFAULT 0,
  rejected_reason TEXT NULL,
  FOREIGN KEY (inbox_id) REFERENCES inboxes(id)
);
