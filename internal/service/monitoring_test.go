package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"webhook-catcher/internal/domain"
	"webhook-catcher/internal/repository/memory"
)

func TestMonitoringQueryValidation(t *testing.T) {
	store := memory.New()
	svc := New(store, "", 1024, 30)
	min := int64(10)
	max := int64(1)
	_, err := svc.GetMonitoringSummary(context.Background(), domain.MonitoringQuery{MinBodySize: &min, MaxBodySize: &max})
	if !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
}

func TestMonitoringInboxHealthStates(t *testing.T) {
	store := memory.New()
	svc := New(store, "", 1024, 30)
	now := time.Now().UTC()

	out1, err := svc.CreateInbox(context.Background(), CreateInboxInput{Name: "A", Now: now, RequestHost: "example.com", RequestProto: "https"})
	if err != nil {
		t.Fatalf("create inbox A: %v", err)
	}
	out2, err := svc.CreateInbox(context.Background(), CreateInboxInput{Name: "B", Now: now, RequestHost: "example.com", RequestProto: "https"})
	if err != nil {
		t.Fatalf("create inbox B: %v", err)
	}

	_, err = svc.RecordHookObservation(context.Background(), HookObservationInput{
		ObservedAt: now,
		InboxID:    out1.Inbox.ID,
		Method:     "POST",
		Path:       "/hook/x",
		StatusCode: 204,
		LatencyMs:  50,
		RemoteIP:   "127.0.0.1",
		Country:    "BR",
	})
	if err != nil {
		t.Fatalf("record obs 1: %v", err)
	}
	_, err = svc.RecordHookObservation(context.Background(), HookObservationInput{
		ObservedAt: now.Add(1 * time.Second),
		InboxID:    out1.Inbox.ID,
		Method:     "POST",
		Path:       "/hook/x",
		StatusCode: 500,
		LatencyMs:  700,
		RemoteIP:   "127.0.0.1",
		Country:    "BR",
	})
	if err != nil {
		t.Fatalf("record obs 2: %v", err)
	}

	health, err := svc.GetMonitoringInboxHealth(context.Background(), domain.MonitoringQuery{From: now.Add(-1 * time.Minute), To: now.Add(1 * time.Minute)})
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	if len(health) != 2 {
		t.Fatalf("expected 2 inboxes, got %d", len(health))
	}

	states := map[string]string{}
	for _, item := range health {
		states[item.InboxID] = item.State
	}
	if states[out1.Inbox.ID] != "unstable" {
		t.Fatalf("expected inbox A unstable, got %q", states[out1.Inbox.ID])
	}
	if states[out2.Inbox.ID] != "no_traffic" {
		t.Fatalf("expected inbox B no_traffic, got %q", states[out2.Inbox.ID])
	}
}
