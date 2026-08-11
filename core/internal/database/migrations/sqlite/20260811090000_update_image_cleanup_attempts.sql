-- +goose Up
-- An image Docker refuses to remove stays pending. Without a count of the
-- refusals, every automatic run reissued the same doomed ImageRemove for it,
-- for as long as the row existed - and the row existed forever, because the
-- history purge only ever touched rows already marked removed.
ALTER TABLE update_image_cleanups ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE update_image_cleanups DROP COLUMN attempts;
