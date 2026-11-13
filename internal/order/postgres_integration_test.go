//go:build integration

package order

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Y1le/hajimi-payment-service/internal/database"
)

func TestPostgresCreateGetAndIdempotency(t *testing.T) {
	dsn := os.Getenv("PAYMENT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("PAYMENT_TEST_DATABASE_URL is required for integration tests; use a dedicated test database")
	}
	ctx := context.Background()
	pool1, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool1.Close)
	pool2, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool2.Close)
	s1, err := NewPostgresStore(pool1, "it_orders")
	if err != nil {
		t.Fatal(err)
	}
	s2, err := NewPostgresStore(pool2, "it_orders")
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Check(ctx); err != nil {
		t.Fatal(err)
	}
	req := CreateRequest{MerchantOrderNo: "parallel_" + strconv.FormatInt(time.Now().UnixNano(), 10), UserID: 42, Amount: "10.00", Currency: "USD"}
	const callers = 64
	type result struct {
		order    Order
		replayed bool
		err      error
	}
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			request := req
			store := s1
			if i%2 == 0 {
				request.Amount = "10"
				store = s2
			}
			created, replayed, err := store.Create(ctx, request)
			results <- result{created, replayed, err}
		}(i)
	}
	wg.Wait()
	close(results)
	var original Order
	newCount := 0
	for got := range results {
		if got.err != nil {
			t.Fatal(got.err)
		}
		if original.ID == "" {
			original = got.order
		}
		if got.order != original || got.order.AmountCents != 1000 || got.order.Status != StatusPending {
			t.Fatalf("inconsistent replay: %+v, original %+v", got.order, original)
		}
		if !got.replayed {
			newCount++
		}
	}
	var count int
	if err := pool1.QueryRow(ctx, `SELECT count(*) FROM public.payment_orders WHERE merchant_id=$1 AND merchant_order_no=$2`, "it_orders", req.MerchantOrderNo).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 || newCount != 1 {
		t.Fatalf("rows=%d first-create responses=%d, want one each", count, newCount)
	}

	for _, field := range []string{"user", "amount"} {
		changed := req
		if field == "user" {
			changed.UserID++
		} else {
			changed.Amount = "20.00"
		}
		if _, _, err := s1.Create(ctx, changed); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed %s: want conflict, got %v", field, err)
		}
	}

	other, err := NewPostgresStore(pool1, "it_other_merchant")
	if err != nil {
		t.Fatal(err)
	}
	otherOrder, replayed, err := other.Create(ctx, req)
	if err != nil || replayed || otherOrder.ID == original.ID {
		t.Fatalf("merchant namespace failed: %+v, %v, %v", otherOrder, replayed, err)
	}
	if _, err := other.Get(ctx, original.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-merchant lookup: %v", err)
	}
	if _, err := s1.Get(ctx, otherOrder.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reverse cross-merchant lookup: %v", err)
	}

	var beforeExpiry, beforeUpdated time.Time
	var beforeVersion int64
	if _, err := pool1.Exec(ctx, `UPDATE public.payment_orders SET payment_status='EXPIRED' WHERE id=$1`, original.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool1.QueryRow(ctx, `SELECT expires_at, updated_at, version FROM public.payment_orders WHERE id=$1`, original.ID).Scan(&beforeExpiry, &beforeUpdated, &beforeVersion); err != nil {
		t.Fatal(err)
	}
	recovered, replayed, err := s2.Create(ctx, req)
	if err != nil || !replayed || recovered.ID != original.ID || recovered.Status != "EXPIRED" {
		t.Fatalf("expired order was reopened: %+v, %v, %v", recovered, replayed, err)
	}
	var afterExpiry, afterUpdated time.Time
	var afterVersion int64
	if err := pool1.QueryRow(ctx, `SELECT expires_at, updated_at, version FROM public.payment_orders WHERE id=$1`, original.ID).Scan(&afterExpiry, &afterUpdated, &afterVersion); err != nil {
		t.Fatal(err)
	}
	if !beforeExpiry.Equal(afterExpiry) || !beforeUpdated.Equal(afterUpdated) || beforeVersion != afterVersion {
		t.Fatal("retry changed expiry, updated_at or version")
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := s1.Create(canceled, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request lost context error: %v", err)
	}
	pool2.Close()
	if _, err := s2.Get(ctx, original.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed pool must be unavailable, got %v", err)
	}
	pool3, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool3.Close)
	s3, err := NewPostgresStore(pool3, "it_orders")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s3.Get(ctx, original.ID)
	if err != nil || got.ID != original.ID || got.Status != "EXPIRED" {
		t.Fatalf("order lost after replacing pool: %+v, %v", got, err)
	}
}
