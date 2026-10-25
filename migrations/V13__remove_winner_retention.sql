ALTER TABLE administration_settings DROP COLUMN retention_days;

-- Старая форма получения призов содержала четвертую строку со сроком хранения.
-- Сбрасываем только ее незавершенный ввод и подтверждение.
DELETE FROM dialogs
WHERE state IN ('admin:input', 'admin:confirm') AND payload->>'action' = 'policies';
