package order

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// MemoryStore is a development adapter. Restarting the process loses all orders
// and their idempotency records; production must use durable storage.
type MemoryStore struct {
	mu           sync.Mutex
	byID         map[string]Order
	byMerchantNo map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		byID:         make(map[string]Order),
		byMerchantNo: make(map[string]string),
	}
}

// Create returns an order and whether this call replayed an earlier request.
// The lookup and insert share one lock so concurrent retries cannot duplicate it.
func (s *MemoryStore) Create(req CreateRequest) (Order, bool, error) {
	cents, err := validate(req)
	if err != nil {
		return Order{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if id, exists := s.byMerchantNo[req.MerchantOrderNo]; exists {
		existing := s.byID[id]
		if existing.UserID != req.UserID || existing.AmountCents != cents || existing.Currency != req.Currency {
			return Order{}, false, ErrConflict
		}
		return existing, true, nil
	}

	var id string
	for {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return Order{}, false, fmt.Errorf("generate order ID: %w", err)
		}
		id = "pay_" + hex.EncodeToString(random[:])
		if _, exists := s.byID[id]; !exists {
			break
		}
	}
	created := Order{
		ID:              id,
		MerchantOrderNo: req.MerchantOrderNo,
		UserID:          req.UserID,
		AmountCents:     cents,
		Currency:        req.Currency,
		Status:          StatusPending,
		CreatedAt:       time.Now().UTC(),
	}
	s.byID[id] = created
	s.byMerchantNo[req.MerchantOrderNo] = id
	return created, false, nil
}

func (s *MemoryStore) Get(id string) (Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found, exists := s.byID[id]
	if !exists {
		return Order{}, ErrNotFound
	}
	return found, nil
}
