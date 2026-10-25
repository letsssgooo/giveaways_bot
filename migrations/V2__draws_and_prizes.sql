CREATE TABLE draws (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    promotion_id smallint NOT NULL DEFAULT 1 REFERENCES promotions(id) ON DELETE RESTRICT,
    kind text NOT NULL CHECK (kind IN ('daily', 'final')),
    partner_day_id bigint,
    scheduled_at timestamptz NOT NULL,
    performed_at timestamptz,
    approved_at timestamptz,
    approved_by bigint REFERENCES users(id) ON DELETE RESTRICT,
    UNIQUE (id, kind),
    UNIQUE (id, partner_day_id),
    FOREIGN KEY (partner_day_id, promotion_id)
        REFERENCES partner_days(id, promotion_id) ON DELETE RESTRICT,
    CHECK ((kind = 'daily') = (partner_day_id IS NOT NULL)),
    CHECK (performed_at IS NULL OR performed_at >= scheduled_at),
    CHECK (num_nonnulls(approved_at, approved_by) IN (0, 2)),
    CHECK (approved_at IS NULL OR (performed_at IS NOT NULL AND approved_at >= performed_at))
);

CREATE UNIQUE INDEX draws_one_daily ON draws(partner_day_id) WHERE kind = 'daily';
CREATE UNIQUE INDEX draws_one_final ON draws(promotion_id) WHERE kind = 'final';

CREATE TABLE prizes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    draw_id bigint NOT NULL REFERENCES draws(id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (btrim(name) <> ''),
    instructions text NOT NULL DEFAULT '',
    places_count integer NOT NULL CHECK (places_count > 0),
    units_per_winner integer NOT NULL CHECK (units_per_winner > 0),
    UNIQUE (id, draw_id)
);

CREATE TABLE draw_participants (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    draw_id bigint NOT NULL,
    draw_kind text NOT NULL,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    registration_id bigint,
    partner_day_id bigint,
    weight smallint NOT NULL CHECK (weight BETWEEN 1 AND 10),
    UNIQUE (draw_id, user_id),
    UNIQUE (id, draw_id),
    UNIQUE (registration_id),
    FOREIGN KEY (draw_id, draw_kind) REFERENCES draws(id, kind) ON DELETE RESTRICT,
    FOREIGN KEY (draw_id, partner_day_id)
        REFERENCES draws(id, partner_day_id) ON DELETE RESTRICT,
    FOREIGN KEY (registration_id, user_id, partner_day_id)
        REFERENCES registrations(id, user_id, partner_day_id) ON DELETE RESTRICT,
    CHECK (
        (draw_kind = 'daily' AND registration_id IS NOT NULL
            AND partner_day_id IS NOT NULL AND weight = 1)
        OR
        (draw_kind = 'final' AND registration_id IS NULL AND partner_day_id IS NULL)
    )
);

CREATE TABLE draw_results (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    draw_id bigint NOT NULL,
    draw_participant_id bigint NOT NULL UNIQUE,
    role text NOT NULL CHECK (role IN ('winner', 'reserve')),
    position integer NOT NULL CHECK (position > 0),
    prize_id bigint,
    place_number integer,
    UNIQUE (draw_id, role, position),
    UNIQUE (id, draw_id, role),
    UNIQUE (id, draw_id, prize_id, place_number),
    FOREIGN KEY (draw_participant_id, draw_id)
        REFERENCES draw_participants(id, draw_id) ON DELETE RESTRICT,
    FOREIGN KEY (prize_id, draw_id) REFERENCES prizes(id, draw_id) ON DELETE RESTRICT,
    CHECK (
        (role = 'winner' AND prize_id IS NOT NULL
            AND place_number IS NOT NULL AND place_number > 0)
        OR
        (role = 'reserve' AND prize_id IS NULL AND place_number IS NULL)
    )
);

CREATE UNIQUE INDEX draw_results_one_winner_per_place
    ON draw_results(prize_id, place_number) WHERE role = 'winner';

CREATE TABLE prize_awards (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    draw_id bigint NOT NULL,
    draw_result_id bigint NOT NULL UNIQUE,
    prize_id bigint NOT NULL,
    place_number integer NOT NULL CHECK (place_number > 0),
    previous_award_id bigint UNIQUE,
    is_current boolean NOT NULL DEFAULT true,
    assigned_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    notified_at timestamptz,
    response_deadline timestamptz,
    confirmed_at timestamptz,
    first_name text CHECK (first_name IS NULL OR btrim(first_name) <> ''),
    last_name text CHECK (last_name IS NULL OR btrim(last_name) <> ''),
    consent_given_at timestamptz,
    consent_version text CHECK (consent_version IS NULL OR btrim(consent_version) <> ''),
    required_result_role text GENERATED ALWAYS AS (
        CASE WHEN previous_award_id IS NULL THEN 'winner' ELSE 'reserve' END
    ) STORED,
    initial_result_id bigint GENERATED ALWAYS AS (
        CASE WHEN previous_award_id IS NULL THEN draw_result_id END
    ) STORED,
    UNIQUE (id, draw_id, prize_id, place_number),
    FOREIGN KEY (draw_result_id, draw_id, required_result_role)
        REFERENCES draw_results(id, draw_id, role) ON DELETE RESTRICT,
    FOREIGN KEY (initial_result_id, draw_id, prize_id, place_number)
        REFERENCES draw_results(id, draw_id, prize_id, place_number) ON DELETE RESTRICT,
    FOREIGN KEY (prize_id, draw_id) REFERENCES prizes(id, draw_id) ON DELETE RESTRICT,
    FOREIGN KEY (previous_award_id, draw_id, prize_id, place_number)
        REFERENCES prize_awards(id, draw_id, prize_id, place_number) ON DELETE RESTRICT,
    CHECK (previous_award_id IS NULL OR previous_award_id <> id),
    CHECK (
        confirmed_at IS NULL OR
        (is_current AND first_name IS NOT NULL AND last_name IS NOT NULL
            AND consent_given_at IS NOT NULL AND consent_version IS NOT NULL)
    )
);

CREATE UNIQUE INDEX prize_awards_one_current_per_place
    ON prize_awards(prize_id, place_number) WHERE is_current;
CREATE INDEX prize_awards_pending_deadline
    ON prize_awards(response_deadline) WHERE is_current AND confirmed_at IS NULL;

CREATE FUNCTION preserve_draw_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    target_draw_id bigint;
    finished_at timestamptz;
BEGIN
    IF TG_OP = 'DELETE' THEN
        target_draw_id := OLD.draw_id;
    ELSE
        target_draw_id := NEW.draw_id;
    END IF;
    IF TG_OP = 'UPDATE' AND NEW.draw_id IS DISTINCT FROM OLD.draw_id THEN
        RAISE EXCEPTION 'snapshot cannot move to another draw' USING ERRCODE = '23514';
    END IF;
    SELECT performed_at INTO finished_at FROM draws WHERE id = target_draw_id FOR SHARE;
    IF finished_at IS NOT NULL THEN
        RAISE EXCEPTION 'performed draw snapshot is immutable' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER draw_participants_preserve_snapshot
    BEFORE INSERT OR UPDATE OR DELETE ON draw_participants
    FOR EACH ROW EXECUTE FUNCTION preserve_draw_snapshot();
CREATE TRIGGER draw_results_preserve_snapshot
    BEFORE INSERT OR UPDATE OR DELETE ON draw_results
    FOR EACH ROW EXECUTE FUNCTION preserve_draw_snapshot();

CREATE FUNCTION preserve_draw_execution() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.performed_at IS NOT NULL AND NEW.performed_at IS DISTINCT FROM OLD.performed_at THEN
        RAISE EXCEPTION 'draw execution cannot be reset' USING ERRCODE = '23514';
    END IF;
    IF OLD.approved_at IS NOT NULL AND
        ROW(NEW.approved_at, NEW.approved_by) IS DISTINCT FROM ROW(OLD.approved_at, OLD.approved_by)
    THEN
        RAISE EXCEPTION 'draw approval cannot be replaced' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER draws_preserve_execution BEFORE UPDATE ON draws
    FOR EACH ROW EXECUTE FUNCTION preserve_draw_execution();

CREATE FUNCTION preserve_award_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF ROW(NEW.draw_id, NEW.draw_result_id, NEW.prize_id, NEW.place_number, NEW.previous_award_id, NEW.assigned_at)
        IS DISTINCT FROM
        ROW(OLD.draw_id, OLD.draw_result_id, OLD.prize_id, OLD.place_number, OLD.previous_award_id, OLD.assigned_at)
    THEN
        RAISE EXCEPTION 'prize assignment identity is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER prize_awards_preserve_identity BEFORE UPDATE ON prize_awards
    FOR EACH ROW EXECUTE FUNCTION preserve_award_identity();
