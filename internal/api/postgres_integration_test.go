//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/Y1le/hajimi-payment-service/internal/database"
	"github.com/Y1le/hajimi-payment-service/internal/order"
)

func TestPostgresHTTPPersistenceAndStorageFailure(t *testing.T) {
	dsn := os.Getenv("PAYMENT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("PAYMENT_TEST_DATABASE_URL is required for integration tests; use a dedicated test database")
	}
	ctx := context.Background()
	pool, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := order.NewPostgresStore(pool, "it_http")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(store, testAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	created := request(handler, http.MethodPost, "/api/v1/orders", validOrderJSON, testAPIKey)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var original struct {
		Order    order.Order `json:"order"`
		Replayed bool        `json:"replayed"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	path := created.Header().Get("Location")
	pool.Close()
	failed := request(handler, http.MethodGet, path, "", testAPIKey)
	if failed.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable storage must return 503, got %d %s", failed.Code, failed.Body.String())
	}
	newPool, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(newPool.Close)
	newStore, err := order.NewPostgresStore(newPool, "it_http")
	if err != nil {
		t.Fatal(err)
	}
	newHandler, err := New(newStore, testAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	got := request(newHandler, http.MethodGet, path, "", testAPIKey)
	var recovered struct {
		Order order.Order `json:"order"`
	}
	if got.Code != http.StatusOK {
		t.Fatalf("recover: %d %s", got.Code, got.Body.String())
	}
	if err := json.Unmarshal(got.Body.Bytes(), &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Order != original.Order {
		t.Fatalf("order changed after reopening pool: %+v", recovered.Order)
	}
	replay := request(newHandler, http.MethodPost, "/api/v1/orders", validOrderJSON, testAPIKey)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay after reopen: %d %s", replay.Code, replay.Body.String())
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &original); err != nil {
		t.Fatal(err)
	}
	if !original.Replayed || original.Order != recovered.Order {
		t.Fatal("retry after reopen did not recover original order")
	}
}
