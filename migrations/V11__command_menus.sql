-- Служебное обновление меню не является сообщением и может возникать при
-- запуске приложения, без входящего события, приза или рассылки.
ALTER TABLE outbox_messages
    DROP CONSTRAINT outbox_messages_source,
    ADD CONSTRAINT outbox_messages_source CHECK (
        num_nonnulls(update_id, prize_award_id, broadcast_id) = 1
        OR (kind = 'sync_commands' AND num_nonnulls(update_id, prize_award_id, broadcast_id) = 0)
    );

-- История позволяет сбросить меню и у удаленного из конфигурации организатора.
CREATE INDEX outbox_messages_commands_user ON outbox_messages(user_id)
    WHERE kind = 'sync_commands';
