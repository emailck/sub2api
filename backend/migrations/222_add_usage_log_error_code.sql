ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS error_code VARCHAR(64);

CREATE INDEX IF NOT EXISTS idx_usage_logs_error_code_created_at
    ON usage_logs (error_code, created_at DESC)
    WHERE error_code IS NOT NULL;
