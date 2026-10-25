CREATE TABLE broadcasts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    created_by bigint NOT NULL CHECK (created_by > 0),
    update_id bigint NOT NULL UNIQUE REFERENCES telegram_updates(update_id) ON DELETE RESTRICT,
    audience_kind text NOT NULL CHECK (audience_kind IN ('all', 'partner', 'users')),
    partner_id bigint REFERENCES partners(id) ON DELETE RESTRICT,
    telegram_ids bigint[] NOT NULL DEFAULT '{}',
    message_text text NOT NULL CHECK (btrim(message_text) <> ''),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    queued_at timestamptz,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'queued', 'cancelled')),
    CHECK ((audience_kind = 'partner') = (partner_id IS NOT NULL)),
    CHECK ((audience_kind = 'users') = (cardinality(telegram_ids) > 0)),
    CHECK ((status = 'queued') = (queued_at IS NOT NULL))
);

-- Список фиксируется при предпросмотре: новые участники не попадают в уже созданную рассылку.
CREATE TABLE broadcast_recipients (
    broadcast_id bigint NOT NULL REFERENCES broadcasts(id) ON DELETE RESTRICT,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    PRIMARY KEY (broadcast_id, user_id)
);

ALTER TABLE outbox_messages
    ADD COLUMN broadcast_id bigint REFERENCES broadcasts(id) ON DELETE RESTRICT,
    DROP CONSTRAINT outbox_messages_check,
    ADD CONSTRAINT outbox_messages_source CHECK (num_nonnulls(update_id, prize_award_id, broadcast_id) = 1),
    ADD CONSTRAINT outbox_messages_broadcast_recipient FOREIGN KEY (broadcast_id, user_id)
        REFERENCES broadcast_recipients(broadcast_id, user_id) ON DELETE RESTRICT;

CREATE UNIQUE INDEX outbox_messages_broadcast_user ON outbox_messages(broadcast_id, user_id)
    WHERE broadcast_id IS NOT NULL;

-- Telegram retry_after действует на отправки всего бота и сохраняется при перезапуске.
CREATE TABLE telegram_delivery_state (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    blocked_until timestamptz NOT NULL DEFAULT '-infinity'
);
INSERT INTO telegram_delivery_state DEFAULT VALUES;
