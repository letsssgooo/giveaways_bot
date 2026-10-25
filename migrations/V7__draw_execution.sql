ALTER TABLE draws ADD COLUMN reserve_count integer
    CHECK (reserve_count BETWEEN 0 AND 10000);

-- NULL сохраняет правило по умолчанию: в финале 5, в ежедневном по числу мест.
CREATE FUNCTION preserve_draw_reserve() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.performed_at IS NOT NULL AND NEW.reserve_count IS DISTINCT FROM OLD.reserve_count THEN
        RAISE EXCEPTION 'performed draw reserve cannot change' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER draws_preserve_reserve BEFORE UPDATE ON draws
    FOR EACH ROW EXECUTE FUNCTION preserve_draw_reserve();
