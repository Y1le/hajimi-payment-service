BEGIN;

CREATE TABLE payment_orders (
    id                  TEXT PRIMARY KEY,

    merchant_id         TEXT NOT NULL
                        CHECK (length(merchant_id) BETWEEN 1 AND 64),

    merchant_order_no   VARCHAR(64) NOT NULL
                        CHECK (
                            merchant_order_no ~ '^[A-Za-z0-9_-]{1,64}$'
                        ),

    user_id             BIGINT NOT NULL CHECK (user_id > 0),

    amount_cents        BIGINT NOT NULL CHECK (amount_cents > 0),
    currency            VARCHAR(3) NOT NULL CHECK (currency = 'USD'),

    asset               TEXT NOT NULL CHECK (asset = 'USDT'),
    network             TEXT NOT NULL CHECK (network = 'TRON'),

    payment_status      TEXT NOT NULL DEFAULT 'PENDING'
                        CHECK (
                            payment_status IN (
                                'PENDING',
                                'CONFIRMED',
                                'EXPIRED',
                                'CANCELLED',
                                'MANUAL_REVIEW'
                            )
                        ),

    fulfillment_status  TEXT NOT NULL DEFAULT 'NOT_READY'
                        CHECK (
                            fulfillment_status IN (
                                'NOT_READY',
                                'PENDING',
                                'PROCESSING',
                                'COMPLETED',
                                'RETRY',
                                'MANUAL_REVIEW'
                            )
                        ),

    expires_at          TIMESTAMPTZ NOT NULL,
    confirmed_at        TIMESTAMPTZ,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    version             BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),

    CONSTRAINT payment_orders_merchant_order_unique
        UNIQUE (merchant_id, merchant_order_no),

    CONSTRAINT payment_orders_confirmation_required
        CHECK (
            payment_status <> 'CONFIRMED'
            OR confirmed_at IS NOT NULL
        ),

    CONSTRAINT payment_orders_fulfillment_requires_payment
        CHECK (
            fulfillment_status = 'NOT_READY'
            OR payment_status = 'CONFIRMED'
        )
);

CREATE INDEX payment_orders_pending_expiry_idx
    ON payment_orders (expires_at)
    WHERE payment_status = 'PENDING';

COMMIT;
