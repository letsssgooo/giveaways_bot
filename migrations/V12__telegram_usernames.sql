ALTER TABLE users ADD COLUMN telegram_username text;

-- Имена меняются и могут переходить другим пользователям. Кэш не предполагает
-- уникальности: неоднозначное имя нельзя использовать для выдачи доступа.
CREATE INDEX users_telegram_username ON users(lower(telegram_username))
    WHERE telegram_username IS NOT NULL;
