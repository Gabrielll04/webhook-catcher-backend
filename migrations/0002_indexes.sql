-- Migration: 0002_indexes
CREATE INDEX IF NOT EXISTS idx_captured_requests_inbox_received_at
  ON captured_requests (inbox_id, received_at DESC);

CREATE INDEX IF NOT EXISTS idx_captured_requests_inbox_method_received_at
  ON captured_requests (inbox_id, method, received_at DESC);

CREATE INDEX IF NOT EXISTS idx_captured_requests_inbox_content_type_received_at
  ON captured_requests (inbox_id, content_type, received_at DESC);

CREATE INDEX IF NOT EXISTS idx_inboxes_status_updated_at
  ON inboxes (status, updated_at DESC);
