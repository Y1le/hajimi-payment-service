package order

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidInput = errors.New("invalid order input")
	ErrConflict     = errors.New("merchant order number already has different parameters")
	ErrNotFound     = errors.New("order not found")
	ErrUnavailable  = errors.New("database unavailable")
	amountPattern   = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]{1,2})?$`)
	orderNoPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

const StatusPending = "PENDING"

// CreateRequest is supplied by the trusted merchant backend, not the browser.
// The merchant backend must obtain UserID from its authenticated session.
type CreateRequest struct {
	MerchantOrderNo string `json:"merchant_order_no"`
	UserID          int64  `json:"user_id"`
	Amount          string `json:"amount"`
	Currency        string `json:"currency"`
}

type Order struct {
	ID              string    `json:"id"`
	MerchantOrderNo string    `json:"merchant_order_no"`
	UserID          int64     `json:"user_id"`
	AmountCents     int64     `json:"amount_cents"`
	Currency        string    `json:"currency"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

// ParseUSDCents converts decimal text to integer cents without using floats.
// The accepted range is 0.01 through the largest positive int64 cent value.
func ParseUSDCents(amount string) (int64, error) {
	if len(amount) > 20 || !amountPattern.MatchString(amount) {
		return 0, fmt.Errorf("%w: amount must be a decimal with at most two decimal places", ErrInvalidInput)
	}
	whole, fraction, _ := strings.Cut(amount, ".")
	fraction += strings.Repeat("0", 2-len(fraction))
	cents, err := strconv.ParseInt(whole+fraction, 10, 64)
	if err != nil || cents <= 0 {
		return 0, fmt.Errorf("%w: amount must be positive and within range", ErrInvalidInput)
	}
	return cents, nil
}

func validate(req CreateRequest) (int64, error) {
	if !orderNoPattern.MatchString(req.MerchantOrderNo) {
		return 0, fmt.Errorf("%w: merchant_order_no must contain 1-64 letters, digits, underscores or hyphens", ErrInvalidInput)
	}
	if req.UserID <= 0 {
		return 0, fmt.Errorf("%w: user_id must be positive", ErrInvalidInput)
	}
	if req.Currency != "USD" {
		return 0, fmt.Errorf("%w: currency must be USD", ErrInvalidInput)
	}
	return ParseUSDCents(req.Amount)
}
