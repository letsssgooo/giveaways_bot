-- Значения по умолчанию можно изменить через админку. Настроенные ранее
-- условия и снимки условий уже назначенных призов остаются прежними.
ALTER TABLE administration_settings
    ALTER COLUMN winner_response_hours SET DEFAULT 48,
    ALTER COLUMN response_starts_on SET DEFAULT 'delivered',
    ALTER COLUMN undeliverable_policy SET DEFAULT 'manual';

UPDATE administration_settings
SET winner_response_hours = COALESCE(winner_response_hours, 48),
    response_starts_on = COALESCE(response_starts_on, 'delivered'),
    undeliverable_policy = COALESCE(undeliverable_policy, 'manual');
