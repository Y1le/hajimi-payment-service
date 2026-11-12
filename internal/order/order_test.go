package order

import (
	"errors"
	"math"
	"sync"
	"testing"
)

func TestParseUSDCents(t *testing.T) {
	valid := map[string]int64{
		"10": 1000, "10.0": 1000, "10.00": 1000,
		"0.01": 1, "0.1": 10, "1.23": 123,
		"92233720368547758.07": math.MaxInt64,
	}
	for input, want := range valid {
		t.Run(input, func(t *testing.T) {
			got, err := ParseUSDCents(input)
			if err != nil || got != want {
				t.Fatalf("got (%d, %v), want (%d, nil)", got, err, want)
			}
		})
	}
	for _, input := range []string{
		"", "0", "0.00", "-1", "+1", "abc", "1e2", "NaN", "Inf",
		"1.001", ".01", "1.", "01.00", " 1.00", "1.00 ",
		"92233720368547758.08", "999999999999999999999999",
	} {
		t.Run("invalid_"+input, func(t *testing.T) {
			if _, err := ParseUSDCents(input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected invalid input, got %v", err)
			}
		})
	}
}

func TestConcurrentCreateReplaysOneOrder(t *testing.T) {
	store := NewMemoryStore()
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
			amount := "10.00"
			if i%2 == 0 {
				amount = "10"
			}
			created, replayed, err := store.Create(CreateRequest{
				MerchantOrderNo: "learn_10001", UserID: 42, Amount: amount, Currency: "USD",
			})
			results <- result{created, replayed, err}
		}(i)
	}
	wg.Wait()
	close(results)
	createdCount := 0
	var firstID string
	for got := range results {
		if got.err != nil {
			t.Fatal(got.err)
		}
		if firstID == "" {
			firstID = got.order.ID
		}
		if got.order.ID != firstID || got.order.AmountCents != 1000 || got.order.Status != StatusPending {
			t.Fatalf("unexpected order: %+v", got.order)
		}
		if !got.replayed {
			createdCount++
		}
	}
	if createdCount != 1 || len(store.byID) != 1 {
		t.Fatalf("created %d responses and stored %d orders; want one", createdCount, len(store.byID))
	}
}

func TestConflictingRetryDoesNotChangeOrder(t *testing.T) {
	for _, change := range []string{"user", "amount"} {
		t.Run(change, func(t *testing.T) {
			store := NewMemoryStore()
			req := CreateRequest{MerchantOrderNo: "learn_10001", UserID: 42, Amount: "10.00", Currency: "USD"}
			original, _, err := store.Create(req)
			if err != nil {
				t.Fatal(err)
			}
			if change == "user" {
				req.UserID++
			} else {
				req.Amount = "20.00"
			}
			if _, _, err := store.Create(req); !errors.Is(err, ErrConflict) {
				t.Fatalf("expected conflict, got %v", err)
			}
			got, err := store.Get(original.ID)
			if err != nil || got != original {
				t.Fatalf("original order changed: %+v, %v", got, err)
			}
		})
	}
}
