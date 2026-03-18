package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"webhook-catcher/internal/domain"
)

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) CreateInbox(ctx context.Context, inbox *domain.Inbox) error {
	const q = `
		INSERT INTO inboxes (
			id,name,token,status,description,retention_days,created_at,updated_at,disabled_at,deleted_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, q,
		inbox.ID,
		inbox.Name,
		inbox.Token,
		string(inbox.Status),
		nullableString(inbox.Description),
		nullableInt(inbox.RetentionDays),
		inbox.CreatedAt.UTC().Format(time.RFC3339Nano),
		inbox.UpdatedAt.UTC().Format(time.RFC3339Nano),
		nullableTime(inbox.DisabledAt),
		nullableTime(inbox.DeletedAt),
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

func (s *Store) ListInboxes(ctx context.Context, page domain.Pagination) ([]domain.Inbox, int, error) {
	limit, offset := normalizePagination(page)
	countQ := `SELECT COUNT(*) FROM inboxes WHERE deleted_at IS NULL`
	var total int
	if err := s.db.QueryRowContext(ctx, countQ).Scan(&total); err != nil {
		return nil, 0, err
	}

	q := `
	SELECT id,name,token,status,description,retention_days,created_at,updated_at,disabled_at,deleted_at
	FROM inboxes
	WHERE deleted_at IS NULL
	ORDER BY created_at DESC
	LIMIT ? OFFSET ?`
	rows, err := s.db.QueryContext(ctx, q, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]domain.Inbox, 0, limit)
	for rows.Next() {
		inbox, err := scanInbox(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, inbox)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Store) GetInboxByID(ctx context.Context, id string) (*domain.Inbox, error) {
	q := `
	SELECT id,name,token,status,description,retention_days,created_at,updated_at,disabled_at,deleted_at
	FROM inboxes
	WHERE id = ? AND deleted_at IS NULL`
	row := s.db.QueryRowContext(ctx, q, id)
	inbox, err := scanInbox(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &inbox, nil
}

func (s *Store) GetInboxByToken(ctx context.Context, token string) (*domain.Inbox, error) {
	q := `
	SELECT id,name,token,status,description,retention_days,created_at,updated_at,disabled_at,deleted_at
	FROM inboxes
	WHERE token = ? AND deleted_at IS NULL`
	row := s.db.QueryRowContext(ctx, q, token)
	inbox, err := scanInbox(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	return &inbox, nil
}

func (s *Store) UpdateInbox(ctx context.Context, id string, patch domain.InboxPatch, now time.Time) (*domain.Inbox, error) {
	sets := make([]string, 0, 5)
	args := make([]any, 0, 6)
	if patch.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *patch.Name)
	}
	if patch.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *patch.Description)
	}
	if patch.RetentionDays != nil {
		sets = append(sets, "retention_days = ?")
		args = append(args, *patch.RetentionDays)
	}
	if patch.Status != nil {
		sets = append(sets, "status = ?")
		args = append(args, string(*patch.Status))
		if *patch.Status == domain.InboxStatusDisabled {
			sets = append(sets, "disabled_at = ?")
			args = append(args, now.UTC().Format(time.RFC3339Nano))
		} else {
			sets = append(sets, "disabled_at = NULL")
		}
	}
	if len(sets) == 0 {
		return s.GetInboxByID(ctx, id)
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, now.UTC().Format(time.RFC3339Nano), id)

	q := fmt.Sprintf("UPDATE inboxes SET %s WHERE id = ? AND deleted_at IS NULL", strings.Join(sets, ","))
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, domain.ErrNotFound
	}
	return s.GetInboxByID(ctx, id)
}

func (s *Store) SoftDeleteInbox(ctx context.Context, id string, now time.Time) error {
	q := `UPDATE inboxes SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`
	res, err := s.db.ExecContext(ctx, q, now.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (s *Store) CreateCapturedRequest(ctx context.Context, req *domain.CapturedRequest) error {
	headersJSON, err := json.Marshal(req.Headers)
	if err != nil {
		return err
	}
	queryJSON, err := json.Marshal(req.QueryParams)
	if err != nil {
		return err
	}
	const q = `
		INSERT INTO captured_requests (
			id,inbox_id,method,path,query_params_json,headers_json,body_raw,body_size_bytes,
			content_type,remote_ip,user_agent,received_at,request_hash,truncated_body,rejected_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err = s.db.ExecContext(ctx, q,
		req.ID,
		req.InboxID,
		req.Method,
		req.Path,
		string(queryJSON),
		string(headersJSON),
		req.BodyRaw,
		req.BodySizeBytes,
		req.ContentType,
		req.RemoteIP,
		req.UserAgent,
		req.ReceivedAt.UTC().Format(time.RFC3339Nano),
		req.RequestHash,
		req.TruncatedBody,
		nullableString(req.RejectedReason),
	)
	return err
}

func (s *Store) ListCapturedRequests(ctx context.Context, inboxID string, filter domain.ListFilter, page domain.Pagination) ([]domain.CapturedRequestSummary, int, error) {
	where := []string{"inbox_id = ?"}
	args := []any{inboxID}

	if filter.Method != "" {
		where = append(where, "method = ?")
		args = append(args, strings.ToUpper(filter.Method))
	}
	if filter.ContentType != "" {
		where = append(where, "content_type = ?")
		args = append(args, filter.ContentType)
	}
	if filter.PathContains != "" {
		where = append(where, "LOWER(path) LIKE ?")
		args = append(args, "%"+strings.ToLower(filter.PathContains)+"%")
	}
	if filter.RemoteIP != "" {
		where = append(where, "remote_ip = ?")
		args = append(args, filter.RemoteIP)
	}
	if filter.UserAgentContains != "" {
		where = append(where, "LOWER(user_agent) LIKE ?")
		args = append(args, "%"+strings.ToLower(filter.UserAgentContains)+"%")
	}
	if filter.RequestHash != "" {
		where = append(where, "request_hash = ?")
		args = append(args, filter.RequestHash)
	}
	if filter.HasBody != nil {
		if *filter.HasBody {
			where = append(where, "body_size_bytes > 0")
		} else {
			where = append(where, "body_size_bytes = 0")
		}
	}
	if filter.MinBodySize != nil {
		where = append(where, "body_size_bytes >= ?")
		args = append(args, *filter.MinBodySize)
	}
	if filter.MaxBodySize != nil {
		where = append(where, "body_size_bytes <= ?")
		args = append(args, *filter.MaxBodySize)
	}
	if filter.From != nil {
		where = append(where, "received_at >= ?")
		args = append(args, filter.From.UTC().Format(time.RFC3339Nano))
	}
	if filter.To != nil {
		where = append(where, "received_at <= ?")
		args = append(args, filter.To.UTC().Format(time.RFC3339Nano))
	}
	if filter.TextQuery != "" {
		needle := "%" + strings.ToLower(filter.TextQuery) + "%"
		where = append(where, "(LOWER(path) LIKE ? OR LOWER(user_agent) LIKE ? OR LOWER(headers_json) LIKE ? OR LOWER(query_params_json) LIKE ? OR LOWER(CAST(body_raw AS TEXT)) LIKE ?)")
		args = append(args, needle, needle, needle, needle, needle)
	}

	whereClause := strings.Join(where, " AND ")
	countQ := fmt.Sprintf("SELECT COUNT(*) FROM captured_requests WHERE %s", whereClause)
	var total int
	if err := s.db.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	order := "DESC"
	if strings.EqualFold(filter.Order, "asc") {
		order = "ASC"
	}
	limit, offset := normalizePagination(page)
	queryQ := fmt.Sprintf(`
		SELECT id,method,path,content_type,body_size_bytes,remote_ip,received_at
		FROM captured_requests
		WHERE %s
		ORDER BY received_at %s
		LIMIT ? OFFSET ?`, whereClause, order)
	argsWithPage := append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, queryQ, argsWithPage...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items := make([]domain.CapturedRequestSummary, 0, limit)
	for rows.Next() {
		var item domain.CapturedRequestSummary
		var received string
		if err := rows.Scan(
			&item.ID,
			&item.Method,
			&item.Path,
			&item.ContentType,
			&item.BodySizeBytes,
			&item.RemoteIP,
			&received,
		); err != nil {
			return nil, 0, err
		}
		t, err := time.Parse(time.RFC3339Nano, received)
		if err != nil {
			return nil, 0, err
		}
		item.ReceivedAt = t
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Store) GetCapturedRequestByID(ctx context.Context, inboxID, requestID string) (*domain.CapturedRequest, error) {
	q := `
	SELECT id,inbox_id,method,path,query_params_json,headers_json,body_raw,body_size_bytes,
	       content_type,remote_ip,user_agent,received_at,request_hash,truncated_body,rejected_reason
	FROM captured_requests
	WHERE id = ? AND inbox_id = ?`
	row := s.db.QueryRowContext(ctx, q, requestID, inboxID)

	var item domain.CapturedRequest
	var queryJSON string
	var headersJSON string
	var received string
	var rejected sql.NullString
	if err := row.Scan(
		&item.ID,
		&item.InboxID,
		&item.Method,
		&item.Path,
		&queryJSON,
		&headersJSON,
		&item.BodyRaw,
		&item.BodySizeBytes,
		&item.ContentType,
		&item.RemoteIP,
		&item.UserAgent,
		&received,
		&item.RequestHash,
		&item.TruncatedBody,
		&rejected,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}

	if err := json.Unmarshal([]byte(queryJSON), &item.QueryParams); err != nil {
		item.QueryParams = map[string]string{}
	}
	if err := json.Unmarshal([]byte(headersJSON), &item.Headers); err != nil {
		item.Headers = map[string]string{}
	}
	t, err := time.Parse(time.RFC3339Nano, received)
	if err != nil {
		return nil, err
	}
	item.ReceivedAt = t
	if rejected.Valid {
		item.RejectedReason = &rejected.String
	}
	return &item, nil
}

func (s *Store) DeleteExpiredCapturedRequests(ctx context.Context, now time.Time, defaultRetentionDays int) (int64, error) {
	q := `
	SELECT r.id, r.received_at, COALESCE(i.retention_days, ?) AS retention_days
	FROM captured_requests r
	JOIN inboxes i ON i.id = r.inbox_id
	WHERE i.deleted_at IS NULL`
	rows, err := s.db.QueryContext(ctx, q, defaultRetentionDays)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		var received string
		var retention int
		if err := rows.Scan(&id, &received, &retention); err != nil {
			return 0, err
		}
		receivedAt, err := time.Parse(time.RFC3339Nano, received)
		if err != nil {
			return 0, err
		}
		if receivedAt.Before(now.UTC().AddDate(0, 0, -retention)) {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	query := `DELETE FROM captured_requests WHERE id = ?`
	var deleted int64
	for _, id := range ids {
		res, err := s.db.ExecContext(ctx, query, id)
		if err != nil {
			return deleted, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return deleted, err
		}
		deleted += n
	}

	monitoringCutoff := now.UTC().AddDate(0, 0, -defaultRetentionDays)
	if _, err := s.db.ExecContext(ctx, "DELETE FROM monitoring_hook_events WHERE observed_at < ?", monitoringCutoff.Format(time.RFC3339Nano)); err != nil {
		return deleted, err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM monitoring_minute_rollups WHERE bucket_minute < ?", monitoringCutoff.Truncate(time.Minute).Format(time.RFC3339)); err != nil {
		return deleted, err
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM monitoring_minute_dimensions WHERE bucket_minute < ?", monitoringCutoff.Truncate(time.Minute).Format(time.RFC3339)); err != nil {
		return deleted, err
	}

	return deleted, nil
}

func (s *Store) CountActiveInboxes(ctx context.Context) (int, error) {
	q := `SELECT COUNT(*) FROM inboxes WHERE deleted_at IS NULL AND status = 'active'`
	var total int
	if err := s.db.QueryRowContext(ctx, q).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanInbox(s scanner) (domain.Inbox, error) {
	var i domain.Inbox
	var status string
	var created string
	var updated string
	var desc sql.NullString
	var retention sql.NullInt64
	var disabled sql.NullString
	var deleted sql.NullString
	if err := s.Scan(
		&i.ID,
		&i.Name,
		&i.Token,
		&status,
		&desc,
		&retention,
		&created,
		&updated,
		&disabled,
		&deleted,
	); err != nil {
		return i, err
	}
	i.Status = domain.InboxStatus(status)
	if desc.Valid {
		i.Description = &desc.String
	}
	if retention.Valid {
		v := int(retention.Int64)
		i.RetentionDays = &v
	}
	createdAt, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return i, err
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return i, err
	}
	i.CreatedAt = createdAt
	i.UpdatedAt = updatedAt
	if disabled.Valid {
		t, err := time.Parse(time.RFC3339Nano, disabled.String)
		if err != nil {
			return i, err
		}
		i.DisabledAt = &t
	}
	if deleted.Valid {
		t, err := time.Parse(time.RFC3339Nano, deleted.String)
		if err != nil {
			return i, err
		}
		i.DeletedAt = &t
	}
	return i, nil
}

func normalizePagination(page domain.Pagination) (int, int) {
	if page.Page <= 0 {
		page.Page = 1
	}
	if page.PageSize <= 0 {
		page.PageSize = 20
	}
	if page.PageSize > 100 {
		page.PageSize = 100
	}
	return page.PageSize, (page.Page - 1) * page.PageSize
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}
