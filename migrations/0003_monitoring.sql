-- Migration: 0003_monitoring
CREATE TABLE IF NOT EXISTS monitoring_hook_events (
  observed_at TEXT NOT NULL,
  bucket_minute TEXT NOT NULL,
  inbox_id TEXT NULL,
  method TEXT NOT NULL,
  path TEXT NOT NULL,
  status_code INTEGER NOT NULL,
  status_class TEXT NOT NULL,
  latency_ms INTEGER NOT NULL,
  body_size_bytes INTEGER NOT NULL,
  has_body INTEGER NOT NULL,
  remote_ip TEXT NOT NULL,
  user_agent TEXT NOT NULL,
  country TEXT NOT NULL,
  content_type TEXT NOT NULL,
  text_blob TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_monitoring_events_observed_at
  ON monitoring_hook_events (observed_at);
CREATE INDEX IF NOT EXISTS idx_monitoring_events_inbox_observed
  ON monitoring_hook_events (inbox_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_monitoring_events_method_observed
  ON monitoring_hook_events (method, observed_at);
CREATE INDEX IF NOT EXISTS idx_monitoring_events_status_observed
  ON monitoring_hook_events (status_class, observed_at);
CREATE INDEX IF NOT EXISTS idx_monitoring_events_bucket
  ON monitoring_hook_events (bucket_minute);

CREATE TABLE IF NOT EXISTS monitoring_minute_rollups (
  bucket_minute TEXT NOT NULL,
  inbox_id TEXT NOT NULL,
  requests_total INTEGER NOT NULL DEFAULT 0,
  errors_total INTEGER NOT NULL DEFAULT 0,
  status_2xx INTEGER NOT NULL DEFAULT 0,
  status_4xx INTEGER NOT NULL DEFAULT 0,
  status_5xx INTEGER NOT NULL DEFAULT 0,
  latency_sum_ms INTEGER NOT NULL DEFAULT 0,
  body_bytes_total INTEGER NOT NULL DEFAULT 0,
  lat_le_10 INTEGER NOT NULL DEFAULT 0,
  lat_le_25 INTEGER NOT NULL DEFAULT 0,
  lat_le_50 INTEGER NOT NULL DEFAULT 0,
  lat_le_100 INTEGER NOT NULL DEFAULT 0,
  lat_le_250 INTEGER NOT NULL DEFAULT 0,
  lat_le_500 INTEGER NOT NULL DEFAULT 0,
  lat_le_1000 INTEGER NOT NULL DEFAULT 0,
  lat_le_2000 INTEGER NOT NULL DEFAULT 0,
  lat_le_5000 INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (bucket_minute, inbox_id)
);

CREATE TABLE IF NOT EXISTS monitoring_minute_dimensions (
  bucket_minute TEXT NOT NULL,
  inbox_id TEXT NOT NULL,
  dimension TEXT NOT NULL,
  value TEXT NOT NULL,
  count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (bucket_minute, inbox_id, dimension, value)
);

CREATE INDEX IF NOT EXISTS idx_monitoring_dims_lookup
  ON monitoring_minute_dimensions (dimension, bucket_minute, inbox_id);
