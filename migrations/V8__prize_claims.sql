ALTER TABLE prize_awards
    ADD COLUMN claim_state text NOT NULL DEFAULT 'pending'
        CHECK (claim_state IN ('pending', 'notified', 'confirmed', 'manual', 'replaced', 'exhausted')),
    ADD COLUMN notice_queued_at timestamptz,
    ADD COLUMN response_hours integer CHECK (response_hours > 0),
    ADD COLUMN response_starts_on text CHECK (response_starts_on IN ('delivered', 'approved')),
    ADD COLUMN undeliverable_policy text CHECK (undeliverable_policy IN ('manual', 'reserve')),
    ADD COLUMN claim_consent_version text REFERENCES consent_versions(version) ON DELETE RESTRICT,
    ADD COLUMN claim_consent_text text,
    ADD CONSTRAINT prize_awards_policy_snapshot CHECK (
        num_nonnulls(response_hours, response_starts_on, undeliverable_policy,
            claim_consent_version, claim_consent_text) IN (0, 5));

UPDATE prize_awards SET claim_state = CASE
    WHEN confirmed_at IS NOT NULL THEN 'confirmed'
    WHEN NOT is_current THEN 'replaced'
    WHEN notified_at IS NOT NULL THEN 'notified'
    ELSE 'pending' END;

CREATE FUNCTION preserve_award_claim_policy() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.response_hours IS NOT NULL AND
        ROW(NEW.response_hours, NEW.response_starts_on, NEW.undeliverable_policy,
            NEW.claim_consent_version, NEW.claim_consent_text)
        IS DISTINCT FROM
        ROW(OLD.response_hours, OLD.response_starts_on, OLD.undeliverable_policy,
            OLD.claim_consent_version, OLD.claim_consent_text)
    THEN
        RAISE EXCEPTION 'prize claim policy is immutable' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER prize_awards_preserve_claim_policy BEFORE UPDATE ON prize_awards
    FOR EACH ROW EXECUTE FUNCTION preserve_award_claim_policy();

CREATE INDEX outbox_messages_award ON outbox_messages(prize_award_id)
    WHERE prize_award_id IS NOT NULL;
