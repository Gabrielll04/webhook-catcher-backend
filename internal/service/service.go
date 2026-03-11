package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"webhook-catcher/internal/domain"
	"webhook-catcher/internal/infra/id"
	"webhook-catcher/internal/repository"
)

type Services struct {
	store                repository.Store
	publicBaseURL        string
	maxPayloadBytes      int64
	defaultRetentionDays int
}

func New(store repository.Store, publicBaseURL string, maxPayloadBytes int64, defaultRetentionDays int) *Services {
	return &Services{
		store:                store,
		publicBaseURL:        strings.TrimSuffix(publicBaseURL, "/"),
		maxPayloadBytes:      maxPayloadBytes,
		defaultRetentionDays: defaultRetentionDays,
	}
}

type CreateInboxInput struct {
	Name          string
	Description   *string
	RetentionDays *int
	Now           time.Time
	RequestHost   string
	RequestProto  string
}

type CreateInboxOutput struct {
	Inbox            domain.Inbox `json:"inbox"`
	PublicCaptureURL string       `json:"public_capture_url"`
}

func (s *Services) CreateInbox(ctx context.Context, in CreateInboxInput) (*CreateInboxOutput, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, domain.ErrInvalidInput
	}
	if in.RetentionDays != nil && *in.RetentionDays <= 0 {
		return nil, domain.ErrInvalidInput
	}
	idValue, err := id.NewULID(in.Now)
	if err != nil {
		return nil, err
	}
	token, err := id.NewToken()
	if err != nil {
		return nil, err
	}
	now := in.Now.UTC()
	inbox := domain.Inbox{
		ID:            idValue,
		Name:          strings.TrimSpace(in.Name),
		Token:         token,
		Status:        domain.InboxStatusActive,
		Description:   in.Description,
		RetentionDays: in.RetentionDays,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.store.CreateInbox(ctx, &inbox); err != nil {
		return nil, err
	}
	return &CreateInboxOutput{
		Inbox:            inbox,
		PublicCaptureURL: s.captureURL(inbox.Token, in.RequestHost, in.RequestProto),
	}, nil
}

func (s *Services) ListInboxes(ctx context.Context, page domain.Pagination) ([]domain.Inbox, int, error) {
	return s.store.ListInboxes(ctx, page)
}

func (s *Services) GetInbox(ctx context.Context, id string) (*domain.Inbox, error) {
	if strings.TrimSpace(id) == "" {
		return nil, domain.ErrInvalidInput
	}
	return s.store.GetInboxByID(ctx, id)
}

func (s *Services) UpdateInbox(ctx context.Context, id string, patch domain.InboxPatch, now time.Time) (*domain.Inbox, error) {
	if patch.Name != nil && strings.TrimSpace(*patch.Name) == "" {
		return nil, domain.ErrInvalidInput
	}
	if patch.Status != nil && *patch.Status != domain.InboxStatusActive && *patch.Status != domain.InboxStatusDisabled {
		return nil, domain.ErrInvalidInput
	}
	if patch.RetentionDays != nil && *patch.RetentionDays <= 0 {
		return nil, domain.ErrInvalidInput
	}
	if patch.Name != nil {
		trimmed := strings.TrimSpace(*patch.Name)
		patch.Name = &trimmed
	}
	return s.store.UpdateInbox(ctx, id, patch, now)
}

func (s *Services) DeleteInbox(ctx context.Context, id string, now time.Time) error {
	return s.store.SoftDeleteInbox(ctx, id, now)
}

type CaptureInput struct {
	Token       string
	Method      string
	Path        string
	QueryParams map[string]string
	Headers     map[string]string
	BodyRaw     []byte
	ContentType string
	RemoteIP    string
	UserAgent   string
	ReceivedAt  time.Time
}

func (s *Services) CaptureRequest(ctx context.Context, in CaptureInput) error {
	if int64(len(in.BodyRaw)) > s.maxPayloadBytes {
		return domain.ErrPayloadTooLarge
	}
	inbox, err := s.store.GetInboxByToken(ctx, in.Token)
	if err != nil {
		return err
	}
	if inbox.Status == domain.InboxStatusDisabled {
		return domain.ErrInboxDisabled
	}
	idValue, err := id.NewULID(in.ReceivedAt)
	if err != nil {
		return err
	}
	h := sha256.Sum256(in.BodyRaw)
	requestHash := hex.EncodeToString(h[:])
	req := domain.CapturedRequest{
		ID:            idValue,
		InboxID:       inbox.ID,
		Method:        strings.ToUpper(in.Method),
		Path:          in.Path,
		QueryParams:   in.QueryParams,
		Headers:       in.Headers,
		BodyRaw:       in.BodyRaw,
		BodySizeBytes: int64(len(in.BodyRaw)),
		ContentType:   in.ContentType,
		RemoteIP:      in.RemoteIP,
		UserAgent:     in.UserAgent,
		ReceivedAt:    in.ReceivedAt.UTC(),
		RequestHash:   requestHash,
		TruncatedBody: false,
	}
	if err := s.store.CreateCapturedRequest(ctx, &req); err != nil {
		return err
	}
	return nil
}

func (s *Services) ListRequests(ctx context.Context, inboxID string, filter domain.ListFilter, page domain.Pagination) ([]domain.CapturedRequestSummary, int, error) {
	if _, err := s.store.GetInboxByID(ctx, inboxID); err != nil {
		return nil, 0, err
	}
	if filter.Order != "" && !strings.EqualFold(filter.Order, "asc") && !strings.EqualFold(filter.Order, "desc") {
		return nil, 0, domain.ErrInvalidInput
	}
	if filter.Order == "" {
		filter.Order = "desc"
	}
	return s.store.ListCapturedRequests(ctx, inboxID, filter, page)
}

func (s *Services) GetRequestDetails(ctx context.Context, inboxID, requestID string) (*domain.Inbox, *domain.CapturedRequest, error) {
	inbox, err := s.store.GetInboxByID(ctx, inboxID)
	if err != nil {
		return nil, nil, err
	}
	req, err := s.store.GetCapturedRequestByID(ctx, inboxID, requestID)
	if err != nil {
		return nil, nil, err
	}
	return inbox, req, nil
}

func (s *Services) RunRetention(ctx context.Context, now time.Time) (int64, error) {
	return s.store.DeleteExpiredCapturedRequests(ctx, now, s.defaultRetentionDays)
}

func (s *Services) Ready(ctx context.Context) error {
	return s.store.Ping(ctx)
}

func (s *Services) ActiveInboxes(ctx context.Context) (int, error) {
	return s.store.CountActiveInboxes(ctx)
}

func (s *Services) captureURL(token, requestHost, requestProto string) string {
	base := s.publicBaseURL
	if base == "" {
		proto := requestProto
		if proto == "" {
			proto = "https"
		}
		base = fmt.Sprintf("%s://%s", proto, requestHost)
	}
	return fmt.Sprintf("%s/hook/%s", strings.TrimSuffix(base, "/"), token)
}

func ExtractHeaders(r *http.Request) map[string]string {
	result := make(map[string]string, len(r.Header))
	for k, vv := range r.Header {
		result[strings.ToLower(k)] = strings.Join(vv, ",")
	}
	return result
}

func ExtractQueryParams(r *http.Request) map[string]string {
	result := make(map[string]string, len(r.URL.Query()))
	for k, vv := range r.URL.Query() {
		result[k] = strings.Join(vv, ",")
	}
	return result
}
