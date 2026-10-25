CREATE TABLE users (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    telegram_user_id bigint NOT NULL UNIQUE CHECK (telegram_user_id > 0),
    chat_id bigint NOT NULL CHECK (chat_id <> 0),
    started_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE promotions (
    id smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    name text NOT NULL CHECK (btrim(name) <> ''),
    starts_on date NOT NULL,
    ends_on date NOT NULL,
    rules text NOT NULL DEFAULT '',
    participation_instructions text NOT NULL DEFAULT '',
    banner_ref text NOT NULL DEFAULT '',
    CHECK (starts_on <= ends_on)
);

CREATE TABLE partners (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL CHECK (btrim(name) <> ''),
    telegram_chat_id bigint NOT NULL CHECK (telegram_chat_id <> 0),
    subscription_url text NOT NULL CHECK (btrim(subscription_url) <> '')
);

CREATE TABLE partner_days (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    promotion_id smallint NOT NULL DEFAULT 1 REFERENCES promotions(id) ON DELETE RESTRICT,
    partner_id bigint NOT NULL REFERENCES partners(id) ON DELETE RESTRICT,
    date date NOT NULL,
    opens_at timestamptz NOT NULL,
    closes_at timestamptz NOT NULL,
    promo_instructions text NOT NULL DEFAULT '',
    promo_valid_until timestamptz,
    UNIQUE (promotion_id, partner_id, date),
    UNIQUE (id, promotion_id),
    UNIQUE (id, date),
    UNIQUE (id, partner_id, date),
    CHECK (opens_at < closes_at),
    CHECK ((opens_at AT TIME ZONE 'Europe/Moscow')::date = date),
    CHECK ((closes_at AT TIME ZONE 'Europe/Moscow') <= (date + 1)::timestamp)
);

CREATE TABLE promo_codes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    partner_id bigint NOT NULL REFERENCES partners(id) ON DELETE RESTRICT,
    code text NOT NULL CHECK (btrim(code) <> ''),
    user_id bigint REFERENCES users(id) ON DELETE RESTRICT,
    partner_day_id bigint,
    issued_at timestamptz,
    issued_date date GENERATED ALWAYS AS (
        (issued_at AT TIME ZONE 'Europe/Moscow')::date
    ) STORED,
    UNIQUE (partner_id, code),
    FOREIGN KEY (partner_day_id, partner_id, issued_date)
        REFERENCES partner_days(id, partner_id, date) ON DELETE RESTRICT,
    CHECK (num_nonnulls(user_id, partner_day_id, issued_at) IN (0, 3))
);

CREATE UNIQUE INDEX promo_codes_one_issue_per_user_day
    ON promo_codes(user_id, partner_day_id) WHERE user_id IS NOT NULL;
CREATE INDEX promo_codes_available
    ON promo_codes(partner_id, id) WHERE user_id IS NULL;

CREATE TABLE promo_code_requests (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    partner_day_id bigint NOT NULL REFERENCES partner_days(id) ON DELETE RESTRICT,
    update_id bigint NOT NULL UNIQUE,
    requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    outcome text NOT NULL CHECK (outcome IN ('issued', 'repeated', 'exhausted', 'closed'))
);

CREATE TABLE registrations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    partner_day_id bigint NOT NULL,
    registered_at timestamptz NOT NULL,
    subscription_checked_at timestamptz NOT NULL,
    registered_date date GENERATED ALWAYS AS (
        (registered_at AT TIME ZONE 'Europe/Moscow')::date
    ) STORED,
    UNIQUE (user_id, partner_day_id),
    UNIQUE (id, user_id, partner_day_id),
    FOREIGN KEY (partner_day_id, registered_date)
        REFERENCES partner_days(id, date) ON DELETE RESTRICT,
    CHECK (subscription_checked_at <= registered_at)
);

CREATE TABLE active_days (
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    date date NOT NULL,
    activated_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, date),
    CHECK (date = (activated_at AT TIME ZONE 'Europe/Moscow')::date)
);

CREATE INDEX users_started_at ON users(started_at);
CREATE INDEX registrations_partner_day ON registrations(partner_day_id);
CREATE INDEX promo_code_requests_requested_at ON promo_code_requests(requested_at);

CREATE FUNCTION preserve_first_start() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.started_at := OLD.started_at;
    RETURN NEW;
END;
$$;
CREATE TRIGGER users_preserve_first_start BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION preserve_first_start();

CREATE FUNCTION preserve_code_assignment() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.user_id IS NOT NULL AND
        ROW(NEW.partner_id, NEW.code, NEW.user_id, NEW.partner_day_id, NEW.issued_at)
        IS DISTINCT FROM
        ROW(OLD.partner_id, OLD.code, OLD.user_id, OLD.partner_day_id, OLD.issued_at)
    THEN
        RAISE EXCEPTION 'issued promo code is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER promo_codes_preserve_assignment BEFORE UPDATE ON promo_codes
    FOR EACH ROW EXECUTE FUNCTION preserve_code_assignment();

CREATE FUNCTION preserve_participation_day() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF ROW(NEW.partner_id, NEW.date) IS DISTINCT FROM ROW(OLD.partner_id, OLD.date)
        AND (
            EXISTS (SELECT 1 FROM registrations WHERE partner_day_id = OLD.id)
            OR EXISTS (SELECT 1 FROM promo_codes WHERE partner_day_id = OLD.id)
        )
    THEN
        RAISE EXCEPTION 'partner and date with participation are immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER partner_days_preserve_participation BEFORE UPDATE ON partner_days
    FOR EACH ROW EXECUTE FUNCTION preserve_participation_day();
