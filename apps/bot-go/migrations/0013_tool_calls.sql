ALTER TABLE bot_processing_log ADD COLUMN IF NOT EXISTS tool_calls jsonb;
