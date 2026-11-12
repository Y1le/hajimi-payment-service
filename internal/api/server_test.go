package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Y1le/hajimi-payment-service/internal/order"
)

const testAPIKey = "test-only-merchant-api-key-32-bytes"
const validOrderJSON = `{"merchant_order_no":"learn_10001","user_id":42,"amount":"10.00","currency":"USD"}`

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	handler, err := New(order.NewMemoryStore(), testAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func request(handler http.Handler, method, path, body, apiKey string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestCreateReplayConflictAndGet(t *testing.T) {
	handler := newTestHandler(t)
	created := request(handler, http.MethodPost, "/api/v1/orders", validOrderJSON, testAPIKey)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var body struct {
		Order    order.Order `json:"order"`
		Replayed bool        `json:"replayed"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Replayed || body.Order.ID == "" || body.Order.AmountCents != 1000 || body.Order.Status != order.StatusPending {
		t.Fatalf("unexpected create response: %+v", body)
	}
	path := created.Header().Get("Location")
	if path != "/api/v1/orders/"+body.Order.ID {
		t.Fatalf("unexpected Location: %s", path)
	}
	replay := request(handler, http.MethodPost, "/api/v1/orders", validOrderJSON, testAPIKey)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	var replayBody struct {
		Order    order.Order `json:"order"`
		Replayed bool        `json:"replayed"`
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &replayBody); err != nil {
		t.Fatal(err)
	}
	if !replayBody.Replayed || replayBody.Order != body.Order {
		t.Fatalf("retry did not return original order: %+v", replayBody)
	}
	conflict := request(handler, http.MethodPost, "/api/v1/orders", strings.Replace(validOrderJSON, "10.00", "20.00", 1), testAPIKey)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting retry: %d %s", conflict.Code, conflict.Body.String())
	}
	got := request(handler, http.MethodGet, path, "", testAPIKey)
	if got.Code != http.StatusOK {
		t.Fatalf("get: %d %s", got.Code, got.Body.String())
	}
	if err := json.Unmarshal(got.Body.Bytes(), &replayBody); err != nil {
		t.Fatal(err)
	}
	if replayBody.Order != body.Order {
		t.Fatalf("get returned different order: %+v", replayBody.Order)
	}
}

func TestMerchantAuthentication(t *testing.T) {
	handler := newTestHandler(t)
	for _, key := range []string{"", "wrong-api-key"} {
		for _, endpoint := range []struct{ method, path, body string }{
			{http.MethodPost, "/api/v1/orders", validOrderJSON},
			{http.MethodGet, "/api/v1/orders/pay_unknown", ""},
		} {
			got := request(handler, endpoint.method, endpoint.path, endpoint.body, key)
			if got.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s without valid key: %d", endpoint.method, endpoint.path, got.Code)
			}
		}
	}
	if got := request(handler, http.MethodGet, "/healthz", "", ""); got.Code != http.StatusOK {
		t.Fatalf("health: %d", got.Code)
	}
}

func TestRejectMalformedOrders(t *testing.T) {
	handler := newTestHandler(t)
	invalidBodies := []string{
		strings.Replace(validOrderJSON, `"10.00"`, `10.00`, 1),
		strings.Replace(validOrderJSON, "10.00", "0.00", 1),
		strings.Replace(validOrderJSON, "10.00", "-1.00", 1),
		strings.Replace(validOrderJSON, "10.00", "0.001", 1),
		strings.Replace(validOrderJSON, "USD", "CNY", 1),
		strings.Replace(validOrderJSON, `"user_id":42`, `"user_id":0`, 1),
		strings.Replace(validOrderJSON, `"user_id":42`, `"user_id":-1`, 1),
		strings.Replace(validOrderJSON, "learn_10001", "", 1),
		strings.TrimSuffix(validOrderJSON, "}") + `,"status":"PAID"}`,
		validOrderJSON + `{}`,
		validOrderJSON + `garbage`,
		`null`, `[]`, ``,
	}
	for _, body := range invalidBodies {
		got := request(handler, http.MethodPost, "/api/v1/orders", body, testAPIKey)
		if got.Code != http.StatusBadRequest {
			t.Errorf("body %q: got %d, want 400; response %s", body, got.Code, got.Body.String())
		}
	}
	largeBody := strings.Replace(validOrderJSON, "learn_10001", strings.Repeat("a", 17<<10), 1)
	if got := request(handler, http.MethodPost, "/api/v1/orders", largeBody, testAPIKey); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body: %d %s", got.Code, got.Body.String())
	}
	if got := request(handler, http.MethodGet, "/api/v1/orders/pay_missing", "", testAPIKey); got.Code != http.StatusNotFound {
		t.Fatalf("missing order: %d", got.Code)
	}
}
