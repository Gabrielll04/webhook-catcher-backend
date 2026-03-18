package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"webhook-catcher/internal/domain"
)

type Store struct {
	mu       sync.RWMutex
	inboxes  map[string]domain.Inbox
	requests map[string]domain.CapturedRequest
}

func New() *Store {
	return &Store{
		inboxes:  make(map[string]domain.Inbox),
		requests: make(map[string]domain.CapturedRequest),
	}
}

func (s *Store) CreateInbox(_ context.Context, inbox *domain.Inbox) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, i := range s.inboxes {
		if i.Token == inbox.Token && i.DeletedAt == nil {
			return domain.ErrConflict
		}
	}
	s.inboxes[inbox.ID] = *inbox
	return nil
}

func (s *Store) ListInboxes(_ context.Context, page domain.Pagination) ([]domain.Inbox, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.Inbox, 0, len(s.inboxes))
	for _, i := range s.inboxes {
		if i.DeletedAt == nil {
			items = append(items, i)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	total := len(items)
	return paginateInboxes(items, page), total, nil
}

func (s *Store) GetInboxByID(_ context.Context, id string) (*domain.Inbox, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.inboxes[id]
	if !ok || item.DeletedAt != nil {
		return nil, domain.ErrNotFound
	}
	copyItem := item
	return &copyItem, nil
}

func (s *Store) GetInboxByToken(_ context.Context, token string) (*domain.Inbox, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.inboxes {
		if item.Token == token && item.DeletedAt == nil {
			copyItem := item
			return &copyItem, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (s *Store) UpdateInbox(_ context.Context, id string, patch domain.InboxPatch, now time.Time) (*domain.Inbox, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.inboxes[id]
	if !ok || item.DeletedAt != nil {
		return nil, domain.ErrNotFound
	}
	if patch.Name != nil {
		item.Name = *patch.Name
	}
	if patch.Description != nil {
		item.Description = patch.Description
	}
	if patch.RetentionDays != nil {
		item.RetentionDays = patch.RetentionDays
	}
	if patch.Status != nil {
		item.Status = *patch.Status
		if *patch.Status == domain.InboxStatusDisabled {
			t := now.UTC()
			item.DisabledAt = &t
		} else {
			item.DisabledAt = nil
		}
	}
	item.UpdatedAt = now.UTC()
	s.inboxes[id] = item
	copyItem := item
	return &copyItem, nil
}

func (s *Store) SoftDeleteInbox(_ context.Context, id string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.inboxes[id]
	if !ok || item.DeletedAt != nil {
		return domain.ErrNotFound
	}
	t := now.UTC()
	item.DeletedAt = &t
	item.UpdatedAt = t
	s.inboxes[id] = item
	return nil
}

func (s *Store) CreateCapturedRequest(_ context.Context, req *domain.CapturedRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests[req.ID] = *req
	return nil
}

func (s *Store) ListCapturedRequests(_ context.Context, inboxID string, filter domain.ListFilter, page domain.Pagination) ([]domain.CapturedRequestSummary, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := make([]domain.CapturedRequestSummary, 0)
	items := make([]domain.CapturedRequest, 0)
	for _, req := range s.requests {
		if req.InboxID != inboxID {
			continue
		}
		if filter.Method != "" && !strings.EqualFold(req.Method, filter.Method) {
			continue
		}
		if filter.ContentType != "" && req.ContentType != filter.ContentType {
			continue
		}
		if filter.PathContains != "" && !strings.Contains(strings.ToLower(req.Path), strings.ToLower(filter.PathContains)) {
			continue
		}
		if filter.RemoteIP != "" && req.RemoteIP != filter.RemoteIP {
			continue
		}
		if filter.UserAgentContains != "" && !strings.Contains(strings.ToLower(req.UserAgent), strings.ToLower(filter.UserAgentContains)) {
			continue
		}
		if filter.RequestHash != "" && req.RequestHash != filter.RequestHash {
			continue
		}
		if filter.HasBody != nil {
			hasBody := req.BodySizeBytes > 0
			if *filter.HasBody != hasBody {
				continue
			}
		}
		if filter.MinBodySize != nil && req.BodySizeBytes < *filter.MinBodySize {
			continue
		}
		if filter.MaxBodySize != nil && req.BodySizeBytes > *filter.MaxBodySize {
			continue
		}
		if filter.From != nil && req.ReceivedAt.Before(*filter.From) {
			continue
		}
		if filter.To != nil && req.ReceivedAt.After(*filter.To) {
			continue
		}
		if filter.TextQuery != "" && !matchesTextQuery(req, filter.TextQuery) {
			continue
		}
		items = append(items, req)
	}
	orderAsc := strings.EqualFold(filter.Order, "asc")
	sort.Slice(items, func(i, j int) bool {
		if orderAsc {
			return items[i].ReceivedAt.Before(items[j].ReceivedAt)
		}
		return items[i].ReceivedAt.After(items[j].ReceivedAt)
	})

	total := len(items)
	for _, item := range paginateRequests(items, page) {
		rows = append(rows, domain.CapturedRequestSummary{
			ID:            item.ID,
			Method:        item.Method,
			Path:          item.Path,
			ContentType:   item.ContentType,
			BodySizeBytes: item.BodySizeBytes,
			RemoteIP:      item.RemoteIP,
			ReceivedAt:    item.ReceivedAt,
		})
	}
	return rows, total, nil
}

func (s *Store) GetCapturedRequestByID(_ context.Context, inboxID, requestID string) (*domain.CapturedRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.requests[requestID]
	if !ok || item.InboxID != inboxID {
		return nil, domain.ErrNotFound
	}
	copyItem := item
	return &copyItem, nil
}

func (s *Store) DeleteExpiredCapturedRequests(_ context.Context, now time.Time, defaultRetentionDays int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var deleted int64
	for id, req := range s.requests {
		inbox, ok := s.inboxes[req.InboxID]
		if !ok {
			continue
		}
		days := defaultRetentionDays
		if inbox.RetentionDays != nil && *inbox.RetentionDays > 0 {
			days = *inbox.RetentionDays
		}
		cutoff := now.UTC().AddDate(0, 0, -days)
		if req.ReceivedAt.Before(cutoff) {
			delete(s.requests, id)
			deleted++
		}
	}
	return deleted, nil
}

func (s *Store) CountActiveInboxes(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, item := range s.inboxes {
		if item.DeletedAt == nil && item.Status == domain.InboxStatusActive {
			total++
		}
	}
	return total, nil
}

func (s *Store) Ping(context.Context) error {
	return nil
}

func matchesTextQuery(req domain.CapturedRequest, query string) bool {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return true
	}
	if strings.Contains(strings.ToLower(req.Path), needle) {
		return true
	}
	if strings.Contains(strings.ToLower(req.ContentType), needle) {
		return true
	}
	if strings.Contains(strings.ToLower(req.UserAgent), needle) {
		return true
	}
	for k, v := range req.Headers {
		if strings.Contains(strings.ToLower(k), needle) || strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	for k, v := range req.QueryParams {
		if strings.Contains(strings.ToLower(k), needle) || strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	return strings.Contains(strings.ToLower(string(req.BodyRaw)), needle)
}
func paginateInboxes(items []domain.Inbox, page domain.Pagination) []domain.Inbox {
	start, end := rangeFromPage(len(items), page)
	if start >= len(items) {
		return []domain.Inbox{}
	}
	return items[start:end]
}

func paginateRequests(items []domain.CapturedRequest, page domain.Pagination) []domain.CapturedRequest {
	start, end := rangeFromPage(len(items), page)
	if start >= len(items) {
		return []domain.CapturedRequest{}
	}
	return items[start:end]
}

func rangeFromPage(total int, page domain.Pagination) (int, int) {
	if page.Page <= 0 {
		page.Page = 1
	}
	if page.PageSize <= 0 {
		page.PageSize = 20
	}
	start := (page.Page - 1) * page.PageSize
	end := start + page.PageSize
	if end > total {
		end = total
	}
	return start, end
}
