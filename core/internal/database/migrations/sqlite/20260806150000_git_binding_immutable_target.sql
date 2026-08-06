-- +goose Up
-- A Folder Link's local and Git endpoints define its synchronization scope.
-- Policy/automation updates must never silently retarget an existing link.
-- +goose StatementBegin
CREATE TRIGGER git_stack_bindings_immutable_target
BEFORE UPDATE OF repository_uuid, host, stack_path, sub_path ON git_stack_bindings
WHEN OLD.repository_uuid <> NEW.repository_uuid
  OR OLD.host <> NEW.host
  OR OLD.stack_path <> NEW.stack_path
  OR OLD.sub_path <> NEW.sub_path
BEGIN
  SELECT RAISE(ABORT, 'folder link target is immutable; unlink and create a new link');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS git_stack_bindings_immutable_target;
