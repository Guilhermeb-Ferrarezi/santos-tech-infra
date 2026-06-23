CREATE TABLE IF NOT EXISTS cron_jobs (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          TEXT        NOT NULL,
    description   TEXT        NOT NULL DEFAULT '',
    schedule_cron TEXT        NOT NULL,
    timezone      TEXT        NOT NULL DEFAULT 'America/Sao_Paulo',
    enabled       BOOLEAN     NOT NULL DEFAULT TRUE,
    action_kind   TEXT        NOT NULL CHECK (action_kind IN ('catalog','http')),
    action_ref    TEXT        NOT NULL DEFAULT '',
    http_method   TEXT        NOT NULL DEFAULT '',
    http_url      TEXT        NOT NULL DEFAULT '',
    http_headers  JSONB       NOT NULL DEFAULT '{}'::jsonb,
    http_body     TEXT        NOT NULL DEFAULT '',
    params        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    timeout_secs  INT         NOT NULL DEFAULT 30,
    max_retries   INT         NOT NULL DEFAULT 3,
    next_run_at   TIMESTAMPTZ,
    last_run_at   TIMESTAMPTZ,
    created_by    TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_cron_jobs_due
    ON cron_jobs (next_run_at) WHERE enabled = TRUE;

CREATE TABLE IF NOT EXISTS cron_runs (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id           BIGINT      NOT NULL REFERENCES cron_jobs(id) ON DELETE CASCADE,
    status           TEXT        NOT NULL CHECK (status IN ('running','success','failed','skipped_overlap')),
    attempt          INT         NOT NULL DEFAULT 1,
    http_status      INT,
    response_excerpt TEXT        NOT NULL DEFAULT '',
    error            TEXT        NOT NULL DEFAULT '',
    started_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_cron_runs_job ON cron_runs (job_id, started_at DESC);
