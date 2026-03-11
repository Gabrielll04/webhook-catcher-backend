package tests

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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
	requestID := listBody.Data[0]["id"].(string)

	detailResp := doJSON(t, ts, http.MethodGet, "/v1/inboxes/"+inboxID+"/requests/"+requestID, nil, nil)
	if detailResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", detailResp.StatusCode)
	}
	var details map[string]any
	decodeResp(t, detailResp, &details)
	if _, ok := details["request"]; !ok {
		t.Fatalf("expected request payload in detail response")
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
