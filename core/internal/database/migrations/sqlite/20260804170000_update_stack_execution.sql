-- +goose Up
ALTER TABLE update_execution_results ADD COLUMN target_type TEXT NOT NULL DEFAULT 'container';
ALTER TABLE update_execution_results ADD COLUMN stack_name TEXT NOT NULL DEFAULT '';
ALTER TABLE update_execution_results ADD COLUMN stack_key TEXT NOT NULL DEFAULT '';
ALTER TABLE update_execution_blocks ADD COLUMN target_type TEXT NOT NULL DEFAULT 'container';
ALTER TABLE update_execution_blocks ADD COLUMN stack_name TEXT NOT NULL DEFAULT '';
ALTER TABLE update_execution_blocks ADD COLUMN stack_key TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_update_execution_blocks_stack_key ON update_execution_blocks(stack_key);

-- +goose Down
DROP INDEX IF EXISTS idx_update_execution_blocks_stack_key;
ALTER TABLE update_execution_blocks DROP COLUMN stack_key;
ALTER TABLE update_execution_blocks DROP COLUMN stack_name;
ALTER TABLE update_execution_blocks DROP COLUMN target_type;
ALTER TABLE update_execution_results DROP COLUMN stack_key;
ALTER TABLE update_execution_results DROP COLUMN stack_name;
ALTER TABLE update_execution_results DROP COLUMN target_type;
