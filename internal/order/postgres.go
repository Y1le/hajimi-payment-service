package order

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Each instance is bound to one server-configured merchant. The API key passed
// to api.New must identify this same merchant. Never take it from request JSON.
type PostgresStore struct {
	pool       *pgxpool.Pool
	merchantID string
}

const orderColumns = `id, merchant_order_no, user_id, amount_cents,
	currency, payment_status, created_at`

func NewPostgresStore(pool *pgxpool.Pool, merchantID string) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	if !orderNoPattern.MatchString(merchantID) {
		return nil, errors.New("PAYMENT_MERCHANT_ID must contain 1-64 letters, digits, underscores or hyphens")
	}
	return &PostgresStore{pool: pool, merchantID: merchantID}, nil
}

// Check rejects a missing/incompatible table before serving requests.
func (s *PostgresStore) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := s.pool.Exec(ctx, `SELECT `+orderColumns+` FROM public.payment_orders LIMIT 0`)
	if err != nil {
		return storageError("check payment schema", err)
	}
	return nil
}

func (s *PostgresStore) Create(ctx context.Context, req CreateRequest) (Order, bool, error) {
	cents, err := validate(req)
	if err != nil {
		return Order{}, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Order{}, false, fmt.Errorf("generate order ID: %w", err)
	}
	id := "pay_" + hex.EncodeToString(random[:])

	// One atomic insert. An existing business order is never updated by a retry.
	created, err := scanOrder(s.pool.QueryRow(ctx, `
		INSERT INTO public.payment_orders (
			id, merchant_id, merchant_order_no, user_id, amount_cents,
			currency, asset, network, expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'USDT', 'TRON', now() + interval '30 minutes')
		ON CONFLICT ON CONSTRAINT payment_orders_merchant_order_unique
		DO NOTHING
		RETURNING `+orderColumns,
		id, s.merchantID, req.MerchantOrderNo, req.UserID, cents, req.Currency))
	if err == nil {
		return created, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		// A network error may occur after commit. The caller must keep the same
		// merchant_order_no on retry, so the committed order is recovered.
		return Order{}, false, storageError("insert payment order", err)
	}

	// This separate statement obtains a fresh snapshot after the conflicting
	// INSERT has finished. Do not merge it into a same-snapshot CTE fallback.
	existing, err := scanOrder(s.pool.QueryRow(ctx, `
		SELECT `+orderColumns+` FROM public.payment_orders
		WHERE merchant_id = $1 AND merchant_order_no = $2`,
		s.merchantID, req.MerchantOrderNo))
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, fmt.Errorf("%w: conflicting order could not be recovered", ErrUnavailable)
	}
	if err != nil {
		return Order{}, false, storageError("recover payment order", err)
	}
	if existing.UserID != req.UserID || existing.AmountCents != cents || existing.Currency != req.Currency {
		return Order{}, false, ErrConflict
	}
	return existing, true, nil
}

func (s *PostgresStore) Get(ctx context.Context, id string) (Order, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	found, err := scanOrder(s.pool.QueryRow(ctx, `
		SELECT `+orderColumns+` FROM public.payment_orders
		WHERE merchant_id = $1 AND id = $2`, s.merchantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, storageError("get payment order", err)
	}
	return found, nil
}

func scanOrder(row pgx.Row) (Order, error) {
	var o Order
	if err := row.Scan(&o.ID, &o.MerchantOrderNo, &o.UserID, &o.AmountCents,
		&o.Currency, &o.Status, &o.CreatedAt); err != nil {
		return Order{}, err
	}
	o.CreatedAt = o.CreatedAt.UTC()
	return o, nil
}

func storageError(operation string, err error) error {
	wrapped := fmt.Errorf("%s: %w", operation, err)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return wrapped
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// Connection/resource failures are temporary. SQL and permission errors
		// remain internal errors instead of masquerading as missing orders.
		if strings.HasPrefix(pgErr.Code, "08") || strings.HasPrefix(pgErr.Code, "53") ||
			pgErr.Code == "57P01" || pgErr.Code == "57P02" || pgErr.Code == "57P03" {
			return errors.Join(ErrUnavailable, wrapped)
		}
		return wrapped
	}
	var scanErr pgx.ScanArgError
	if errors.As(err, &scanErr) {
		return wrapped
	}
	// Remaining driver failures include transport failures and a closed pool.
	return errors.Join(ErrUnavailable, wrapped)
}
