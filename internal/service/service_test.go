package service

import (
	"context"
	"testing"
	"time"

	"webhook-catcher/internal/domain"
	"webhook-catcher/internal/repository/memory"
)

func TestCreateInboxAndUpdate(t *testing.T) {
	store := memory.New()
	svc := New(store, "https://api.example.com", 1024, 30)
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)

	retention := 15
	out, err := svc.CreateInbox(context.Background(), CreateInboxInput{
		Name:          "Orders",
		RetentionDays: &retention,
		Now:           now,
		RequestHost:   "api.example.com",
		RequestProto:  "https",
	})
	if err != nil {
		t.Fatalf("create inbox: %v", err)
	}
	if out.Inbox.ID == "" || out.Inbox.Token == "" {
		t.Fatalf("expected generated id/token")
	}
	if out.PublicCaptureURL == "" {
		t.Fatalf("expected capture url")
	}

	newName := "Orders v2"
	status := domain.InboxStatusDisabled
	updated, err := svc.UpdateInbox(context.Background(), out.Inbox.ID, domain.InboxPatch{
		Name:   &newName,
		Status: &status,
	}, now.Add(1*time.Hour))
	if err != nil {
		t.Fatalf("update inbox: %v", err)
	}
	if updated.Name != newName || updated.Status != domain.InboxStatusDisabled || updated.DisabledAt == nil {
		t.Fatalf("unexpected update result: %+v", updated)
	}
}

func TestCapturePayloadTooLarge(t *testing.T) {
	store := memory.New()
	svc := New(store, "", 4, 30)
	now := time.Now().UTC()

	out, err := svc.CreateInbox(context.Background(), CreateInboxInput{Name: "x", Now: now, RequestHost: "example.com", RequestProto: "https"})
	if err != nil {
		t.Fatalf("create inbox: %v", err)
	}

	_, err = svc.CaptureRequest(context.Background(), CaptureInput{
		Token:       out.Inbox.Token,
		Method:      "POST",
		Path:        "/hook/" + out.Inbox.Token,
		BodyRaw:     []byte("12345"),
		Headers:     map[string]string{},
		QueryParams: map[string]string{},
		ReceivedAt:  now,
	})
	if err != domain.ErrPayloadTooLarge {
		t.Fatalf("expected ErrPayloadTooLarge, got %v", err)
	}
}

func TestRetentionUsesInboxOverride(t *testing.T) {
	store := memory.New()
	svc := New(store, "", 1024, 30)
	now := time.Now().UTC()

	retention := 1
	out, err := svc.CreateInbox(context.Background(), CreateInboxInput{Name: "ret", RetentionDays: &retention, Now: now, RequestHost: "example.com", RequestProto: "https"})
	if err != nil {
		t.Fatalf("create inbox: %v", err)
	}

	_, err = svc.CaptureRequest(context.Background(), CaptureInput{
		Token:       out.Inbox.Token,
		Method:      "POST",
		Path:        "/hook/" + out.Inbox.Token,
		BodyRaw:     []byte("ok"),
		Headers:     map[string]string{},
		QueryParams: map[string]string{},
		ReceivedAt:  now.AddDate(0, 0, -2),
	})
	if err != nil {
		t.Fatalf("capture request: %v", err)
	}

	deleted, err := svc.RunRetention(context.Background(), now)
	if err != nil {
		t.Fatalf("run retention: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 deleted, got %d", deleted)
	}
}
