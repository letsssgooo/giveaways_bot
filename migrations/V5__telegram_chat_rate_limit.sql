-- Последняя успешная отправка в чат нужна для ограничения частоты после рестарта.
CREATE INDEX outbox_messages_chat_sent_idx ON outbox_messages (user_id, sent_at DESC)
    WHERE status = 'sent' AND kind <> 'answer_callback_query';
