package tests

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"webhook-catcher/internal/app"
	"webhook-catcher/internal/infra/config"
	"webhook-catcher/internal/repository/memory"
)

func TestEndToEndInboxCaptureAndQuery(t *testing.T) {
	h := app.NewWithStore(config.Config{
		MaxPayloadBytes:      1024 * 1024,
		DefaultRetentionDays: 30,
		AdminRateLimitRPM:    1000,
		RequireAccess:        false,
	}, memory.New())
	ts := httptest.NewServer(h)
	defer ts.Close()

	createResp := doJSON(t, ts, http.MethodPost, "/v1/inboxes", map[string]any{"name": "integration"}, nil)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createResp.StatusCode)
	}
	var created map[string]any
	decodeResp(t, createResp, &created)
	token := created["token"].(string)
	inboxID := created["id"].(string)

	hookReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/"+token+"?source=test", bytes.NewBufferString("{\"hello\":\"world\"}"))
	hookReq.Header.Set("Content-Type", "application/json")
	hookReq.Header.Set("User-Agent", "integration-test")
	hookResp, err := http.DefaultClient.Do(hookReq)
	if err != nil {
		t.Fatalf("hook request failed: %v", err)
	}
	defer hookResp.Body.Close()
	if hookResp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(hookResp.Body)
		t.Fatalf("expected 204, got %d body=%s", hookResp.StatusCode, string(body))
	}

	listResp := doJSON(t, ts, http.MethodGet, "/v1/inboxes/"+inboxID+"/requests?page=1&page_size=10", nil, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", listResp.StatusCode)
	}
	var listBody struct {
		Data []map[string]any `json:"data"`
	}
	decodeResp(t, listResp, &listBody)
	if len(listBody.Data) != 1 {
		t.Fatalf("expected 1 request, got %d", len(listBody.Data))
	}
	if _, ok := listBody.Data[0]["path"].(string); !ok {
		t.Fatalf("expected path in list summary")
	}
	if _, ok := listBody.Data[0]["received_at"].(string); !ok {
		t.Fatalf("expected received_at string in list summary")
	}
	requestID := listBody.Data[0]["id"].(string)

	detailResp := doJSON(t, ts, http.MethodGet, "/v1/inboxes/"+inboxID+"/requests/"+requestID, nil, nil)
	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", detailResp.StatusCode)
	}
	var details map[string]any
	decodeResp(t, detailResp, &details)
	reqObj, ok := details["request"].(map[string]any)
	if !ok {
		t.Fatalf("expected request payload in detail response")
	}
	if _, ok := reqObj["headers"].(map[string]any); !ok {
		t.Fatalf("expected headers in detail response")
	}
	if _, ok := reqObj["body_raw"].(string); !ok {
		t.Fatalf("expected body_raw in detail response")
	}
	if _, ok := reqObj["received_at"].(string); !ok {
		t.Fatalf("expected received_at in detail response")
	}
}

func TestCaptureErrors(t *testing.T) {
	h := app.NewWithStore(config.Config{
		MaxPayloadBytes:      5,
		DefaultRetentionDays: 30,
		AdminRateLimitRPM:    1000,
		RequireAccess:        false,
	}, memory.New())
	ts := httptest.NewServer(h)
	defer ts.Close()

	notFoundResp := doJSON(t, ts, http.MethodPost, "/hook/invalid-token", nil, nil)
	if notFoundResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", notFoundResp.StatusCode)
	}
	assertErrorEnvelope(t, notFoundResp)

	createResp := doJSON(t, ts, http.MethodPost, "/v1/inboxes", map[string]any{"name": "disabled"}, nil)
	var created map[string]any
	decodeResp(t, createResp, &created)
	inboxID := created["id"].(string)
	token := created["token"].(string)

	patchResp := doJSON(t, ts, http.MethodPatch, "/v1/inboxes/"+inboxID, map[string]any{"status": "disabled"}, nil)
	if patchResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 patch, got %d", patchResp.StatusCode)
	}

	disabledResp := doJSON(t, ts, http.MethodPost, "/hook/"+token, map[string]any{}, nil)
	if disabledResp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", disabledResp.StatusCode)
	}
	assertErrorEnvelope(t, disabledResp)

	createResp2 := doJSON(t, ts, http.MethodPost, "/v1/inboxes", map[string]any{"name": "large"}, nil)
	var created2 map[string]any
	decodeResp(t, createResp2, &created2)
	token2 := created2["token"].(string)

	largeResp := doJSON(t, ts, http.MethodPost, "/hook/"+token2, map[string]any{"payload": "123456789"}, nil)
	if largeResp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", largeResp.StatusCode)
	}
}

func TestAdminRequiresAccessWhenEnabled(t *testing.T) {
	h := app.NewWithStore(config.Config{
		MaxPayloadBytes:      1024,
		DefaultRetentionDays: 30,
		AdminRateLimitRPM:    1000,
		RequireAccess:        true,
	}, memory.New())
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp := doJSON(t, ts, http.MethodGet, "/v1/inboxes", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	headers := map[string]string{"CF-Access-Authenticated-User-Email": "dev@example.com"}
	resp2 := doJSON(t, ts, http.MethodGet, "/v1/inboxes", nil, headers)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with access header, got %d", resp2.StatusCode)
	}
}

func TestAdminCORSPreflightAndOriginValidation(t *testing.T) {
	h := app.NewWithStore(config.Config{
		MaxPayloadBytes:      1024,
		DefaultRetentionDays: 30,
		AdminRateLimitRPM:    1000,
		RequireAccess:        true,
		CORSAllowedOrigins:   "http://localhost:5173",
		CORSAllowedMethods:   "GET,POST,PATCH,DELETE,OPTIONS",
		CORSAllowedHeaders:   "Content-Type,Authorization",
		CORSMaxAgeSeconds:    600,
	}, memory.New())
	ts := httptest.NewServer(h)
	defer ts.Close()

	preflightHeaders := map[string]string{
		"Origin":                         "http://localhost:5173",
		"Access-Control-Request-Method":  "GET",
		"Access-Control-Request-Headers": "Content-Type",
	}
	preflightResp := doJSON(t, ts, http.MethodOptions, "/v1/inboxes", nil, preflightHeaders)
	if preflightResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 preflight, got %d", preflightResp.StatusCode)
	}
	if got := preflightResp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("unexpected allow-origin header: %q", got)
	}

	forbiddenHeaders := map[string]string{
		"Origin":                        "http://evil.local",
		"Access-Control-Request-Method": "GET",
	}
	forbiddenResp := doJSON(t, ts, http.MethodOptions, "/v1/inboxes", nil, forbiddenHeaders)
	if forbiddenResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for invalid origin, got %d", forbiddenResp.StatusCode)
	}
	assertErrorEnvelope(t, forbiddenResp)
}

func TestHookOptionsDoesNotPersist(t *testing.T) {
	h := app.NewWithStore(config.Config{
		MaxPayloadBytes:      1024 * 1024,
		DefaultRetentionDays: 30,
		AdminRateLimitRPM:    1000,
		RequireAccess:        false,
	}, memory.New())
	ts := httptest.NewServer(h)
	defer ts.Close()

	createResp := doJSON(t, ts, http.MethodPost, "/v1/inboxes", map[string]any{"name": "hook-options"}, nil)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createResp.StatusCode)
	}
	var created map[string]any
	decodeResp(t, createResp, &created)
	token := created["token"].(string)
	inboxID := created["id"].(string)

	resp := doJSON(t, ts, http.MethodOptions, "/hook/"+token, nil, map[string]string{"Origin": "http://localhost:3000"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	listResp := doJSON(t, ts, http.MethodGet, "/v1/inboxes/"+inboxID+"/requests", nil, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", listResp.StatusCode)
	}
	var listBody struct {
		Data []map[string]any `json:"data"`
	}
	decodeResp(t, listResp, &listBody)
	if len(listBody.Data) != 0 {
		t.Fatalf("expected 0 captured requests, got %d", len(listBody.Data))
	}
}

func TestCreateInboxRejectsEmptyPayload(t *testing.T) {
	h := app.NewWithStore(config.Config{
		MaxPayloadBytes:      1024,
		DefaultRetentionDays: 30,
		AdminRateLimitRPM:    1000,
		RequireAccess:        false,
	}, memory.New())
	ts := httptest.NewServer(h)
	defer ts.Close()

	resp := doJSON(t, ts, http.MethodPost, "/v1/inboxes", nil, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	assertErrorEnvelope(t, resp)
}

func TestCreateInboxIdempotencyKey(t *testing.T) {
	h := app.NewWithStore(config.Config{
		MaxPayloadBytes:      1024,
		DefaultRetentionDays: 30,
		AdminRateLimitRPM:    1000,
		RequireAccess:        false,
	}, memory.New())
	ts := httptest.NewServer(h)
	defer ts.Close()

	headers := map[string]string{"Idempotency-Key": "test-key-1"}
	resp1 := doJSON(t, ts, http.MethodPost, "/v1/inboxes", map[string]any{"name": "idem"}, headers)
	if resp1.StatusCode != http.StatusCreated {
		t.Fatalf("expected first call 201, got %d", resp1.StatusCode)
	}
	var body1 map[string]any
	decodeResp(t, resp1, &body1)

	resp2 := doJSON(t, ts, http.MethodPost, "/v1/inboxes", map[string]any{"name": "idem"}, headers)
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("expected second call 201, got %d", resp2.StatusCode)
	}
	var body2 map[string]any
	decodeResp(t, resp2, &body2)

	if body1["id"] != body2["id"] {
		t.Fatalf("expected same id for idempotent replay")
	}

	listResp := doJSON(t, ts, http.MethodGet, "/v1/inboxes", nil, nil)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", listResp.StatusCode)
	}
	var listBody struct {
		Total float64 `json:"total"`
	}
	decodeResp(t, listResp, &listBody)
	if int(listBody.Total) != 1 {
		t.Fatalf("expected total 1 inbox, got %d", int(listBody.Total))
	}
}

func TestInboxStreamReceivesCapturedRequest(t *testing.T) {
	h := app.NewWithStore(config.Config{
		MaxPayloadBytes:      1024 * 1024,
		DefaultRetentionDays: 30,
		AdminRateLimitRPM:    1000,
		RequireAccess:        false,
	}, memory.New())
	ts := httptest.NewServer(h)
	defer ts.Close()

	createResp := doJSON(t, ts, http.MethodPost, "/v1/inboxes", map[string]any{"name": "stream"}, nil)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createResp.StatusCode)
	}
	var created map[string]any
	decodeResp(t, createResp, &created)
	inboxID := created["id"].(string)
	token := created["token"].(string)

	streamReq, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/inboxes/"+inboxID+"/stream", nil)
	if err != nil {
		t.Fatalf("new stream request: %v", err)
	}
	streamReq.Header.Set("Accept", "text/event-stream")
	streamResp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(streamResp.Body)
		t.Fatalf("expected 200 stream, got %d body=%s", streamResp.StatusCode, string(body))
	}

	dataCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(streamResp.Body)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				errCh <- readErr
				return
			}
			if strings.HasPrefix(line, "data: ") {
				dataCh <- strings.TrimSpace(strings.TrimPrefix(line, "data: "))
				return
			}
		}
	}()

	hookReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/"+token, bytes.NewBufferString("{\"live\":true}"))
	hookReq.Header.Set("Content-Type", "application/json")
	hookResp, err := http.DefaultClient.Do(hookReq)
	if err != nil {
		t.Fatalf("hook request failed: %v", err)
	}
	defer hookResp.Body.Close()
	if hookResp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(hookResp.Body)
		t.Fatalf("expected 204 hook, got %d body=%s", hookResp.StatusCode, string(body))
	}

	select {
	case payload := <-dataCh:
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("invalid stream payload: %v", err)
		}
		if event["inbox_id"] != inboxID {
			t.Fatalf("expected inbox_id %s, got %v", inboxID, event["inbox_id"])
		}
		if event["method"] != http.MethodPost {
			t.Fatalf("expected method POST, got %v", event["method"])
		}
	case readErr := <-errCh:
		t.Fatalf("stream read failed: %v", readErr)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for stream event")
	}
}
func TestExtendedRequestFilters(t *testing.T) {
	h := app.NewWithStore(config.Config{
		MaxPayloadBytes:      1024 * 1024,
		DefaultRetentionDays: 30,
		AdminRateLimitRPM:    1000,
		RequireAccess:        false,
	}, memory.New())
	ts := httptest.NewServer(h)
	defer ts.Close()

	createResp := doJSON(t, ts, http.MethodPost, "/v1/inboxes", map[string]any{"name": "filters"}, nil)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createResp.StatusCode)
	}
	var created map[string]any
	decodeResp(t, createResp, &created)
	inboxID := created["id"].(string)
	token := created["token"].(string)

	reqA, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/"+token+"?source=alpha", bytes.NewBufferString("{\"kind\":\"alpha\"}"))
	reqA.Header.Set("Content-Type", "application/json")
	reqA.Header.Set("User-Agent", "alpha-bot")
	respA, err := http.DefaultClient.Do(reqA)
	if err != nil {
		t.Fatalf("hook request A failed: %v", err)
	}
	respA.Body.Close()
	if respA.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for request A, got %d", respA.StatusCode)
	}

	reqB, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/"+token+"?source=beta", bytes.NewBufferString("{\"kind\":\"beta\",\"tag\":\"needle\"}"))
	reqB.Header.Set("Content-Type", "application/json")
	reqB.Header.Set("User-Agent", "beta-client")
	respB, err := http.DefaultClient.Do(reqB)
	if err != nil {
		t.Fatalf("hook request B failed: %v", err)
	}
	respB.Body.Close()
	if respB.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204 for request B, got %d", respB.StatusCode)
	}

	byUA := doJSON(t, ts, http.MethodGet, "/v1/inboxes/"+inboxID+"/requests?user_agent_contains=beta", nil, nil)
	if byUA.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 user_agent filter, got %d", byUA.StatusCode)
	}
	var byUABody struct {
		Total float64 `json:"total"`
		Data  []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	decodeResp(t, byUA, &byUABody)
	if int(byUABody.Total) != 1 || len(byUABody.Data) != 1 {
		t.Fatalf("expected one result for user_agent filter, got total=%d len=%d", int(byUABody.Total), len(byUABody.Data))
	}

	byText := doJSON(t, ts, http.MethodGet, "/v1/inboxes/"+inboxID+"/requests?q=needle", nil, nil)
	if byText.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 text query, got %d", byText.StatusCode)
	}
	var byTextBody struct {
		Total float64 `json:"total"`
	}
	decodeResp(t, byText, &byTextBody)
	if int(byTextBody.Total) != 1 {
		t.Fatalf("expected one result for q filter, got %d", int(byTextBody.Total))
	}

	bySize := doJSON(t, ts, http.MethodGet, "/v1/inboxes/"+inboxID+"/requests?min_body_size=1&max_body_size=25", nil, nil)
	if bySize.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 size filter, got %d", bySize.StatusCode)
	}
	var bySizeBody struct {
		Total float64 `json:"total"`
	}
	decodeResp(t, bySize, &bySizeBody)
	if int(bySizeBody.Total) == 0 {
		t.Fatalf("expected at least one result for size filter")
	}

	invalidSize := doJSON(t, ts, http.MethodGet, "/v1/inboxes/"+inboxID+"/requests?min_body_size=50&max_body_size=10", nil, nil)
	if invalidSize.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid size range, got %d", invalidSize.StatusCode)
	}
	assertErrorEnvelope(t, invalidSize)
}
func TestRequestsPaginationMetadata(t *testing.T) {
	h := app.NewWithStore(config.Config{
		MaxPayloadBytes:      1024 * 1024,
		DefaultRetentionDays: 30,
		AdminRateLimitRPM:    1000,
		RequireAccess:        false,
	}, memory.New())
	ts := httptest.NewServer(h)
	defer ts.Close()

	createResp := doJSON(t, ts, http.MethodPost, "/v1/inboxes", map[string]any{"name": "pagination"}, nil)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createResp.StatusCode)
	}
	var created map[string]any
	decodeResp(t, createResp, &created)
	inboxID := created["id"].(string)
	token := created["token"].(string)

	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/"+token, bytes.NewBufferString("{\"n\":1}"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("hook request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("expected 204 hook, got %d", resp.StatusCode)
		}
	}

	page1 := doJSON(t, ts, http.MethodGet, "/v1/inboxes/"+inboxID+"/requests?page=1&page_size=2", nil, nil)
	if page1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", page1.StatusCode)
	}
	var page1Body struct {
		Data       []map[string]any `json:"data"`
		Total      float64          `json:"total"`
		TotalPages float64          `json:"total_pages"`
		HasNext    bool             `json:"has_next"`
		HasPrev    bool             `json:"has_prev"`
	}
	decodeResp(t, page1, &page1Body)
	if len(page1Body.Data) != 2 || int(page1Body.Total) != 3 || int(page1Body.TotalPages) != 2 || !page1Body.HasNext || page1Body.HasPrev {
		t.Fatalf("unexpected page1 metadata: len=%d total=%d pages=%d next=%v prev=%v", len(page1Body.Data), int(page1Body.Total), int(page1Body.TotalPages), page1Body.HasNext, page1Body.HasPrev)
	}

	page2 := doJSON(t, ts, http.MethodGet, "/v1/inboxes/"+inboxID+"/requests?page=2&page_size=2", nil, nil)
	if page2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", page2.StatusCode)
	}
	var page2Body struct {
		Data       []map[string]any `json:"data"`
		Total      float64          `json:"total"`
		TotalPages float64          `json:"total_pages"`
		HasNext    bool             `json:"has_next"`
		HasPrev    bool             `json:"has_prev"`
	}
	decodeResp(t, page2, &page2Body)
	if len(page2Body.Data) != 1 || int(page2Body.Total) != 3 || int(page2Body.TotalPages) != 2 || page2Body.HasNext || !page2Body.HasPrev {
		t.Fatalf("unexpected page2 metadata: len=%d total=%d pages=%d next=%v prev=%v", len(page2Body.Data), int(page2Body.Total), int(page2Body.TotalPages), page2Body.HasNext, page2Body.HasPrev)
	}
}
func TestMonitoringEndpointsAndLiveStream(t *testing.T) {
	h := app.NewWithStore(config.Config{
		MaxPayloadBytes:      16,
		DefaultRetentionDays: 30,
		AdminRateLimitRPM:    1000,
		RequireAccess:        false,
	}, memory.New())
	ts := httptest.NewServer(h)
	defer ts.Close()

	createResp := doJSON(t, ts, http.MethodPost, "/v1/inboxes", map[string]any{"name": "monitoring"}, nil)
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", createResp.StatusCode)
	}
	var created map[string]any
	decodeResp(t, createResp, &created)
	inboxID := created["id"].(string)
	token := created["token"].(string)

	streamReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/monitoring/live", nil)
	streamReq.Header.Set("Accept", "text/event-stream")
	streamResp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatalf("open monitoring stream: %v", err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(streamResp.Body)
		t.Fatalf("expected 200 stream, got %d body=%s", streamResp.StatusCode, string(body))
	}

	dataCh := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(streamResp.Body)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				return
			}
			if strings.HasPrefix(line, "data: ") {
				dataCh <- strings.TrimSpace(strings.TrimPrefix(line, "data: "))
				return
			}
		}
	}()

	reqOK, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/"+token, bytes.NewBufferString("{\"ok\":true}"))
	reqOK.Header.Set("Content-Type", "application/json")
	respOK, err := http.DefaultClient.Do(reqOK)
	if err != nil {
		t.Fatalf("send hook ok: %v", err)
	}
	respOK.Body.Close()
	if respOK.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", respOK.StatusCode)
	}

	reqLarge, _ := http.NewRequest(http.MethodPost, ts.URL+"/hook/"+token, bytes.NewBufferString("{\"payload\":\"12345678901234567890\"}"))
	reqLarge.Header.Set("Content-Type", "application/json")
	respLarge, err := http.DefaultClient.Do(reqLarge)
	if err != nil {
		t.Fatalf("send hook large: %v", err)
	}
	respLarge.Body.Close()
	if respLarge.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d", respLarge.StatusCode)
	}

	select {
	case payload := <-dataCh:
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("invalid monitoring stream payload: %v", err)
		}
		if _, ok := event["status_code"].(float64); !ok {
			t.Fatalf("expected status_code in live event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting monitoring live event")
	}

	summaryResp := doJSON(t, ts, http.MethodGet, "/v1/monitoring/summary?inbox_id="+inboxID+"&from=now-15m&to=now", nil, nil)
	if summaryResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 summary, got %d", summaryResp.StatusCode)
	}
	var summary struct {
		RequestsTotal float64        `json:"requests_total"`
		StatusCodes   map[string]any `json:"status_codes"`
	}
	decodeResp(t, summaryResp, &summary)
	if int(summary.RequestsTotal) < 2 {
		t.Fatalf("expected at least 2 requests in summary, got %d", int(summary.RequestsTotal))
	}

	timeseriesResp := doJSON(t, ts, http.MethodGet, "/v1/monitoring/timeseries?inbox_id="+inboxID+"&from=now-15m&to=now", nil, nil)
	if timeseriesResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 timeseries, got %d", timeseriesResp.StatusCode)
	}
	var timeseries struct {
		Data []map[string]any `json:"data"`
	}
	decodeResp(t, timeseriesResp, &timeseries)
	if len(timeseries.Data) == 0 {
		t.Fatalf("expected non-empty timeseries")
	}

	breakdownResp := doJSON(t, ts, http.MethodGet, "/v1/monitoring/breakdown?inbox_id="+inboxID+"&dimension=status&from=now-15m&to=now", nil, nil)
	if breakdownResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 breakdown, got %d", breakdownResp.StatusCode)
	}
	var breakdown struct {
		Data []map[string]any `json:"data"`
	}
	decodeResp(t, breakdownResp, &breakdown)
	if len(breakdown.Data) == 0 {
		t.Fatalf("expected non-empty breakdown")
	}

	healthResp := doJSON(t, ts, http.MethodGet, "/v1/monitoring/inboxes/health?inbox_id="+inboxID, nil, nil)
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 health, got %d", healthResp.StatusCode)
	}
	var health struct {
		Data []map[string]any `json:"data"`
	}
	decodeResp(t, healthResp, &health)
	if len(health.Data) != 1 {
		t.Fatalf("expected one inbox health item, got %d", len(health.Data))
	}
	if state, _ := health.Data[0]["state"].(string); state == "" {
		t.Fatalf("expected state in health response")
	}
}
func doJSON(t *testing.T, ts *httptest.Server, method, path string, body any, headers map[string]string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func decodeResp(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func assertErrorEnvelope(t *testing.T, resp *http.Response) {
	t.Helper()
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing error object")
	}
	if errObj["code"] == "" || errObj["message"] == "" || errObj["request_id"] == "" {
		t.Fatalf("invalid error object: %+v", errObj)
	}
}
