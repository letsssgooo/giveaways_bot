CREATE INDEX telegram_updates_pending
    ON telegram_updates(update_id) WHERE processed_at IS NULL;

-- Earlier unfinished messages block later messages of the same user.
CREATE INDEX outbox_messages_user_pending
    ON outbox_messages(user_id, id) WHERE status IN ('pending', 'processing');
