CREATE TABLE telegram_updates (
    update_id bigint PRIMARY KEY,
    user_id bigint REFERENCES users(id) ON DELETE RESTRICT,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    processed_at timestamptz
);

-- Original payloads are unavailable; historical requests are already processed.
INSERT INTO telegram_updates(update_id, user_id, payload, received_at, processed_at)
SELECT update_id, user_id, '{}'::jsonb, requested_at, requested_at
FROM promo_code_requests;

CREATE TABLE telegram_polling_state (
    id smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    next_offset bigint NOT NULL DEFAULT 0 CHECK (next_offset >= 0)
);
INSERT INTO telegram_polling_state(id) VALUES (1);

ALTER TABLE promo_code_requests ADD CONSTRAINT promo_code_requests_update_fk
    FOREIGN KEY (update_id) REFERENCES telegram_updates(update_id) ON DELETE RESTRICT;

CREATE TABLE dialogs (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE RESTRICT,
    state text NOT NULL CHECK (btrim(state) <> ''),
    payload jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload) = 'object'),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz
);

CREATE TABLE outbox_messages (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    deduplication_key text NOT NULL UNIQUE CHECK (btrim(deduplication_key) <> ''),
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    update_id bigint REFERENCES telegram_updates(update_id) ON DELETE RESTRICT,
    prize_award_id bigint REFERENCES prize_awards(id) ON DELETE RESTRICT,
    kind text NOT NULL CHECK (btrim(kind) <> ''),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'processing', 'sent', 'failed')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    lease_until timestamptz,
    attempt_token uuid,
    sent_at timestamptz,
    CHECK (num_nonnulls(update_id, prize_award_id) = 1),
    CHECK (
        (status = 'processing' AND lease_until IS NOT NULL AND attempt_token IS NOT NULL)
        OR (status <> 'processing' AND lease_until IS NULL AND attempt_token IS NULL)
    ),
    CHECK ((status = 'sent') = (sent_at IS NOT NULL))
);

CREATE INDEX outbox_messages_ready
    ON outbox_messages(available_at, id) WHERE status = 'pending';
CREATE INDEX outbox_messages_expired_lease
    ON outbox_messages(lease_until) WHERE status = 'processing';

CREATE TABLE background_jobs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    deduplication_key text NOT NULL UNIQUE CHECK (btrim(deduplication_key) <> ''),
    kind text NOT NULL CHECK (btrim(kind) <> ''),
    payload jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload) = 'object'),
    available_at timestamptz NOT NULL,
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    lease_until timestamptz,
    attempt_token uuid,
    completed_at timestamptz,
    CHECK (
        (status = 'processing' AND lease_until IS NOT NULL AND attempt_token IS NOT NULL)
        OR (status <> 'processing' AND lease_until IS NULL AND attempt_token IS NULL)
    ),
    CHECK ((status = 'completed') = (completed_at IS NOT NULL))
);

CREATE INDEX background_jobs_ready
    ON background_jobs(available_at, id) WHERE status = 'pending';
CREATE INDEX background_jobs_expired_lease
    ON background_jobs(lease_until) WHERE status = 'processing';
