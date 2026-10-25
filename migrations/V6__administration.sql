CREATE TABLE administration_grants (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    telegram_user_id bigint NOT NULL CHECK (telegram_user_id > 0),
    partner_id bigint REFERENCES partners(id) ON DELETE RESTRICT,
    UNIQUE NULLS NOT DISTINCT (telegram_user_id, partner_id)
);

ALTER TABLE promotions
    ADD COLUMN consent_text text NOT NULL DEFAULT '',
    ADD COLUMN consent_version text NOT NULL DEFAULT '',
    ADD CONSTRAINT promotions_consent_pair CHECK ((btrim(consent_text) = '') = (btrim(consent_version) = ''));

CREATE TABLE administration_settings (
    id smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    winner_response_hours integer CHECK (winner_response_hours > 0),
    response_starts_on text CHECK (response_starts_on IN ('delivered', 'approved')),
    undeliverable_policy text CHECK (undeliverable_policy IN ('manual', 'reserve')),
    retention_days integer DEFAULT 90 CHECK (retention_days > 0)
);
INSERT INTO administration_settings DEFAULT VALUES;

CREATE TABLE consent_versions (
    version text PRIMARY KEY CHECK (btrim(version) <> ''),
    consent_text text NOT NULL CHECK (btrim(consent_text) <> ''),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

ALTER TABLE partner_days ADD COLUMN is_enabled boolean NOT NULL DEFAULT true;

-- Снимок проведенного розыгрыша должен сохранять исходные призы и расписание.
CREATE TRIGGER prizes_preserve_snapshot
    BEFORE INSERT OR UPDATE OR DELETE ON prizes
    FOR EACH ROW EXECUTE FUNCTION preserve_draw_snapshot();

CREATE FUNCTION preserve_draw_configuration() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.performed_at IS NOT NULL AND
        ROW(NEW.promotion_id, NEW.kind, NEW.partner_day_id, NEW.scheduled_at)
        IS DISTINCT FROM ROW(OLD.promotion_id, OLD.kind, OLD.partner_day_id, OLD.scheduled_at)
    THEN
        RAISE EXCEPTION 'performed draw configuration is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER draws_preserve_configuration BEFORE UPDATE ON draws
    FOR EACH ROW EXECUTE FUNCTION preserve_draw_configuration();
