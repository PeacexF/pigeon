-- Pending deliveries. A leased job's run_at is its lease expiry, so a job
-- whose worker died becomes due again without a separate sweep.
CREATE TABLE pigeon_jobs (
    id         text PRIMARY KEY, -- pigeon.Message.ID: event and channel
    event_id   text        NOT NULL,
    event_type text        NOT NULL,
    channel    text        NOT NULL,
    message    jsonb       NOT NULL,
    attempts   integer     NOT NULL DEFAULT 0,
    run_at     timestamptz NOT NULL DEFAULT now(),
    lease      text,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pigeon_jobs_run_at ON pigeon_jobs (run_at);

CREATE TABLE pigeon_idempotency_keys (
    key        text PRIMARY KEY,
    event_id   text        NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX pigeon_idempotency_keys_expires_at ON pigeon_idempotency_keys (expires_at);

CREATE TABLE pigeon_dead_letters (
    id         text PRIMARY KEY,
    event_id   text        NOT NULL,
    event_type text        NOT NULL,
    channel    text        NOT NULL,
    message    jsonb       NOT NULL,
    attempts   integer     NOT NULL,
    error      text        NOT NULL,
    created_at timestamptz NOT NULL,
    buried_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pigeon_dead_letters_buried_at ON pigeon_dead_letters (buried_at);

-- One row per finished attempt; rate-limit deferrals are not attempts.
CREATE TABLE pigeon_attempts (
    id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id   text        NOT NULL,
    event_id text        NOT NULL,
    channel  text        NOT NULL,
    attempt  integer     NOT NULL,
    outcome  text        NOT NULL CHECK (outcome IN ('delivered', 'retry', 'dead')),
    error    text,
    at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pigeon_attempts_job_id ON pigeon_attempts (job_id);
CREATE INDEX pigeon_attempts_at ON pigeon_attempts (at);
