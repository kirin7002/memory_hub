-- MemoryHub phase 1 baseline. Applied transactionally by the migration command.

CREATE TABLE user_stream_heads (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    committed_seq bigint NOT NULL DEFAULT 0 CHECK (committed_seq >= 0),
    min_available_seq bigint NOT NULL DEFAULT 1,
    policy_epoch bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id)
);

CREATE TABLE devices (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    device_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('phone', 'car', 'web')),
    status text NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'revoked')),
    scope_key text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz,
    PRIMARY KEY (tenant_id, user_id, device_id),
    FOREIGN KEY (tenant_id, user_id)
        REFERENCES user_stream_heads (tenant_id, user_id)
);

CREATE TABLE contexts (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    context_id text NOT NULL,
    scope_type text NOT NULL CHECK (scope_type IN ('conversation', 'task')),
    task_id text,
    status text NOT NULL DEFAULT 'active',
    context_version bigint NOT NULL DEFAULT 0,
    summary_version bigint NOT NULL DEFAULT 0,
    source_revision bigint NOT NULL DEFAULT 1,
    latest_input_seq bigint NOT NULL DEFAULT 0,
    latest_change_seq bigint NOT NULL DEFAULT 0,
    state_json jsonb NOT NULL DEFAULT '{}',
    execution_target text NOT NULL DEFAULT 'device'
        CHECK (execution_target IN ('device', 'cloud')),
    fencing_token bigint NOT NULL DEFAULT 0,
    active_run_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    PRIMARY KEY (tenant_id, user_id, context_id),
    FOREIGN KEY (tenant_id, user_id)
        REFERENCES user_stream_heads (tenant_id, user_id),
    CHECK (scope_type <> 'task' OR task_id IS NOT NULL)
);

CREATE TABLE conversations (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    conversation_id text NOT NULL,
    context_id text NOT NULL,
    title text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'archived', 'deleted')),
    head_seq bigint NOT NULL DEFAULT 0,
    latest_change_seq bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    PRIMARY KEY (tenant_id, user_id, conversation_id),
    FOREIGN KEY (tenant_id, user_id, context_id)
        REFERENCES contexts (tenant_id, user_id, context_id)
);

CREATE TABLE context_sources (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    context_id text NOT NULL,
    conversation_id text NOT NULL,
    bound_at_seq bigint NOT NULL,
    removed_at_seq bigint,
    PRIMARY KEY (tenant_id, user_id, context_id, conversation_id),
    FOREIGN KEY (tenant_id, user_id, context_id)
        REFERENCES contexts (tenant_id, user_id, context_id),
    FOREIGN KEY (tenant_id, user_id, conversation_id)
        REFERENCES conversations (tenant_id, user_id, conversation_id)
);

CREATE TABLE context_revisions (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    context_id text NOT NULL,
    version bigint NOT NULL,
    user_seq bigint NOT NULL,
    cause_event_id text,
    schema_version integer NOT NULL DEFAULT 1,
    state_json jsonb NOT NULL,
    execution_target text NOT NULL
        CHECK (execution_target IN ('device', 'cloud')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, context_id, version),
    FOREIGN KEY (tenant_id, user_id, context_id)
        REFERENCES contexts (tenant_id, user_id, context_id)
);

CREATE INDEX conversations_recent_idx
    ON conversations (tenant_id, user_id, updated_at DESC, conversation_id);
CREATE INDEX contexts_active_idx
    ON contexts (tenant_id, user_id, status, updated_at DESC);
CREATE INDEX sources_by_conversation_idx
    ON context_sources (tenant_id, user_id, conversation_id);

CREATE TABLE conversation_events (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    event_id text NOT NULL,
    conversation_id text NOT NULL,
    context_id text NOT NULL,
    user_seq bigint NOT NULL,
    conv_seq bigint NOT NULL,
    event_type text NOT NULL,
    actor_type text NOT NULL CHECK (actor_type IN ('user', 'agent', 'system', 'tool')),
    device_id text,
    run_id text,
    turn_id text,
    reply_to_event_id text,
    status text NOT NULL DEFAULT 'final',
    content_json jsonb NOT NULL,
    metadata_json jsonb NOT NULL DEFAULT '{}',
    source_time timestamptz,
    accepted_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    PRIMARY KEY (tenant_id, user_id, event_id),
    UNIQUE (tenant_id, user_id, conversation_id, conv_seq),
    FOREIGN KEY (tenant_id, user_id, conversation_id)
        REFERENCES conversations (tenant_id, user_id, conversation_id),
    FOREIGN KEY (tenant_id, user_id, context_id)
        REFERENCES contexts (tenant_id, user_id, context_id)
);

CREATE INDEX events_by_context_idx
    ON conversation_events (tenant_id, user_id, context_id, user_seq, event_id);

CREATE TABLE context_summaries (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    context_id text NOT NULL,
    summary_version bigint NOT NULL,
    source_revision bigint NOT NULL,
    covered_user_seq bigint NOT NULL,
    source_event_ids jsonb NOT NULL,
    summary_json jsonb NOT NULL,
    prompt_version text NOT NULL,
    status text NOT NULL CHECK (status IN ('published', 'invalidated')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, context_id, summary_version),
    FOREIGN KEY (tenant_id, user_id, context_id)
        REFERENCES contexts (tenant_id, user_id, context_id)
);

CREATE TABLE sync_changes (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    user_seq bigint NOT NULL,
    change_type text NOT NULL,
    scope_refs jsonb NOT NULL,
    policy_epoch bigint NOT NULL,
    payload_json jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, user_seq),
    FOREIGN KEY (tenant_id, user_id)
        REFERENCES user_stream_heads (tenant_id, user_id)
);

CREATE TABLE mutation_receipts (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    request_id text NOT NULL,
    operation text NOT NULL,
    request_hash bytea NOT NULL,
    response_json jsonb NOT NULL,
    user_seq bigint NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, request_id),
    FOREIGN KEY (tenant_id, user_id)
        REFERENCES user_stream_heads (tenant_id, user_id)
);

CREATE TABLE device_sync_state (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    device_id text NOT NULL,
    scope_key text NOT NULL,
    applied_seq bigint NOT NULL DEFAULT 0,
    policy_epoch bigint NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, device_id, scope_key),
    FOREIGN KEY (tenant_id, user_id, device_id)
        REFERENCES devices (tenant_id, user_id, device_id)
);

CREATE TABLE outbox_jobs (
    job_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    kind text NOT NULL,
    dedupe_key text NOT NULL,
    payload_json jsonb NOT NULL,
    state text NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'done', 'dead')),
    attempts integer NOT NULL DEFAULT 0,
    lease_token bigint NOT NULL DEFAULT 0,
    lease_owner text,
    lease_until timestamptz,
    available_at timestamptz NOT NULL DEFAULT now(),
    last_error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, user_id, kind, dedupe_key),
    FOREIGN KEY (tenant_id, user_id)
        REFERENCES user_stream_heads (tenant_id, user_id)
);

CREATE INDEX jobs_pending_idx ON outbox_jobs (available_at, job_id)
    WHERE state = 'pending';
CREATE INDEX jobs_expired_idx ON outbox_jobs (lease_until, job_id)
    WHERE state = 'running';

CREATE TABLE agent_runs (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    run_id text NOT NULL,
    context_id text NOT NULL,
    trigger_event_id text NOT NULL,
    executor_kind text NOT NULL CHECK (executor_kind IN ('device', 'cloud')),
    executor_device_id text,
    base_context_version bigint NOT NULL,
    accepted_context_version bigint NOT NULL,
    fencing_token bigint NOT NULL,
    attempt integer NOT NULL DEFAULT 1,
    status text NOT NULL CHECK (status IN
        ('running', 'succeeded', 'failed', 'cancelled', 'superseded')),
    lease_owner text NOT NULL,
    lease_until timestamptz NOT NULL,
    checkpoint_ref text,
    last_error_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((executor_kind = 'device' AND executor_device_id IS NOT NULL)
        OR (executor_kind = 'cloud' AND executor_device_id IS NULL)),
    PRIMARY KEY (tenant_id, user_id, run_id),
    FOREIGN KEY (tenant_id, user_id, context_id)
        REFERENCES contexts (tenant_id, user_id, context_id)
);

CREATE UNIQUE INDEX one_running_run_per_context_idx
    ON agent_runs (tenant_id, user_id, context_id)
    WHERE status = 'running';

CREATE TABLE tool_operations (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    operation_id text NOT NULL,
    context_id text NOT NULL,
    run_id text NOT NULL,
    tool_name text NOT NULL,
    input_context_version bigint NOT NULL,
    external_idempotency_key text NOT NULL,
    state text NOT NULL CHECK (state IN
        ('planned', 'awaiting_authorization', 'executing',
         'succeeded', 'failed', 'unknown', 'cancelled')),
    input_json jsonb NOT NULL,
    result_json jsonb,
    observed_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, operation_id),
    UNIQUE (tenant_id, user_id, tool_name, external_idempotency_key),
    FOREIGN KEY (tenant_id, user_id, context_id)
        REFERENCES contexts (tenant_id, user_id, context_id),
    FOREIGN KEY (tenant_id, user_id, run_id)
        REFERENCES agent_runs (tenant_id, user_id, run_id)
);

CREATE TABLE device_executions (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    execution_id text NOT NULL,
    device_id text NOT NULL,
    conversation_id text NOT NULL,
    context_id text NOT NULL,
    base_context_version bigint NOT NULL,
    base_source_revision bigint NOT NULL,
    base_policy_epoch bigint NOT NULL,
    imported_context_version bigint NOT NULL,
    base_was_current boolean NOT NULL,
    status text NOT NULL CHECK (status IN
        ('imported', 'reconciled', 'needs_revalidation', 'rejected')),
    trace_json jsonb NOT NULL,
    proposal_json jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, execution_id),
    FOREIGN KEY (tenant_id, user_id, device_id)
        REFERENCES devices (tenant_id, user_id, device_id),
    FOREIGN KEY (tenant_id, user_id, conversation_id)
        REFERENCES conversations (tenant_id, user_id, conversation_id),
    FOREIGN KEY (tenant_id, user_id, context_id)
        REFERENCES contexts (tenant_id, user_id, context_id)
);

CREATE TABLE deletion_tombstones (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    object_type text NOT NULL,
    object_id text NOT NULL,
    deleted_at_seq bigint NOT NULL,
    deleted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, object_type, object_id),
    FOREIGN KEY (tenant_id, user_id)
        REFERENCES user_stream_heads (tenant_id, user_id)
);

CREATE TABLE input_processing_revisions (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    context_id text NOT NULL,
    event_id text NOT NULL,
    user_seq bigint NOT NULL,
    decision_context_version bigint NOT NULL,
    resolution text NOT NULL CHECK (resolution IN
        ('pending', 'applied', 'superseded', 'needs_clarification')),
    reason_code text NOT NULL,
    evidence_event_ids jsonb NOT NULL DEFAULT '[]',
    proposal_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, context_id, event_id, user_seq),
    FOREIGN KEY (tenant_id, user_id, context_id)
        REFERENCES contexts (tenant_id, user_id, context_id),
    FOREIGN KEY (tenant_id, user_id, event_id)
        REFERENCES conversation_events (tenant_id, user_id, event_id),
    CHECK (jsonb_typeof(evidence_event_ids) = 'array')
);

CREATE INDEX input_latest_resolution_idx
    ON input_processing_revisions
    (tenant_id, user_id, context_id, event_id, user_seq DESC);

CREATE TABLE event_streams (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    origin_id text NOT NULL,
    context_id text NOT NULL,
    owner_kind text NOT NULL CHECK (owner_kind IN ('device', 'cloud')),
    owner_principal_id text NOT NULL,
    device_id text,
    producer_id text NOT NULL,
    max_accepted_stream_seq bigint NOT NULL DEFAULT 0,
    last_accepted_event_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, origin_id),
    FOREIGN KEY (tenant_id, user_id, context_id)
        REFERENCES contexts (tenant_id, user_id, context_id),
    FOREIGN KEY (tenant_id, user_id, device_id)
        REFERENCES devices (tenant_id, user_id, device_id),
    CHECK (max_accepted_stream_seq >= 0),
    CHECK ((owner_kind = 'device' AND device_id IS NOT NULL)
        OR (owner_kind = 'cloud' AND device_id IS NULL))
);

ALTER TABLE conversation_events
    ADD COLUMN origin_id text,
    ADD COLUMN stream_seq bigint CHECK (stream_seq > 0),
    ADD COLUMN boot_id text,
    ADD COLUMN clock_segment_id text,
    ADD COLUMN monotonic_ns bigint CHECK (monotonic_ns >= 0),
    ADD COLUMN calibration_id text,
    ADD CONSTRAINT event_stream_ref
        FOREIGN KEY (tenant_id, user_id, origin_id)
        REFERENCES event_streams (tenant_id, user_id, origin_id),
    ADD CONSTRAINT event_stream_pair
        CHECK ((origin_id IS NULL) = (stream_seq IS NULL));

CREATE UNIQUE INDEX unique_event_stream_seq
    ON conversation_events (tenant_id, user_id, origin_id, stream_seq)
    WHERE origin_id IS NOT NULL;

ALTER TABLE agent_runs
    ADD COLUMN accepted_source_revision bigint NOT NULL DEFAULT 1,
    ADD COLUMN accepted_policy_epoch bigint NOT NULL DEFAULT 1;

CREATE TABLE time_probes (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    probe_id text NOT NULL,
    device_id text NOT NULL,
    boot_id text NOT NULL,
    clock_segment_id text NOT NULL,
    request_hash bytea NOT NULL,
    cloud_instance_id text NOT NULL,
    received_at timestamptz NOT NULL,
    response_marked_at timestamptz NOT NULL,
    server_uncertainty_us bigint CHECK (server_uncertainty_us >= 0),
    cloud_time_quality text NOT NULL CHECK (cloud_time_quality IN
        ('bounded', 'estimated', 'unknown', 'inconsistent')),
    key_id text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, probe_id),
    UNIQUE (tenant_id, user_id, device_id, probe_id),
    FOREIGN KEY (tenant_id, user_id, device_id)
        REFERENCES devices (tenant_id, user_id, device_id),
    CHECK (response_marked_at >= received_at)
);

CREATE TABLE time_calibrations (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    calibration_id text NOT NULL,
    device_id text NOT NULL,
    probe_id text NOT NULL,
    boot_id text NOT NULL,
    clock_segment_id text NOT NULL,
    request_hash bytea NOT NULL,
    raw_samples_json jsonb NOT NULL,
    mono_anchor_ns bigint NOT NULL CHECK (mono_anchor_ns >= 0),
    anchor_utc timestamptz NOT NULL,
    offset_us bigint NOT NULL,
    delay_us bigint NOT NULL CHECK (delay_us >= 0),
    anchor_uncertainty_us bigint CHECK (anchor_uncertainty_us >= 0),
    drift_bound_ppm numeric CHECK (drift_bound_ppm >= 0),
    time_quality text NOT NULL CHECK (time_quality IN
        ('bounded', 'estimated', 'unknown', 'inconsistent')),
    assumptions_json jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, calibration_id),
    UNIQUE (tenant_id, user_id, device_id, calibration_id),
    UNIQUE (tenant_id, user_id, probe_id),
    FOREIGN KEY (tenant_id, user_id, device_id, probe_id)
        REFERENCES time_probes (tenant_id, user_id, device_id, probe_id),
    CHECK (time_quality <> 'bounded' OR anchor_uncertainty_us IS NOT NULL)
);

CREATE INDEX calibrations_by_device_segment_idx
    ON time_calibrations
    (tenant_id, user_id, device_id, boot_id, clock_segment_id, created_at DESC);

ALTER TABLE conversation_events
    ADD CONSTRAINT event_device_calibration_ref
        FOREIGN KEY (tenant_id, user_id, device_id, calibration_id)
        REFERENCES time_calibrations
        (tenant_id, user_id, device_id, calibration_id),
    ADD CONSTRAINT calibration_requires_device
        CHECK (calibration_id IS NULL OR device_id IS NOT NULL);

CREATE TABLE event_time_assessments (
    tenant_id text NOT NULL,
    user_id text NOT NULL,
    assessment_id text NOT NULL,
    event_id text NOT NULL,
    calibration_id text,
    published_user_seq bigint NOT NULL,
    change_event_id text NOT NULL,
    algorithm_version text NOT NULL,
    estimated_occurred_at timestamptz,
    earliest_at timestamptz,
    latest_at timestamptz,
    time_quality text NOT NULL CHECK (time_quality IN
        ('bounded', 'estimated', 'unknown', 'inconsistent')),
    evidence_json jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, user_id, assessment_id),
    UNIQUE (tenant_id, user_id, event_id, published_user_seq),
    FOREIGN KEY (tenant_id, user_id, event_id)
        REFERENCES conversation_events (tenant_id, user_id, event_id),
    FOREIGN KEY (tenant_id, user_id, change_event_id)
        REFERENCES conversation_events (tenant_id, user_id, event_id),
    FOREIGN KEY (tenant_id, user_id, calibration_id)
        REFERENCES time_calibrations (tenant_id, user_id, calibration_id),
    CHECK (
        (time_quality = 'bounded'
         AND estimated_occurred_at IS NOT NULL
         AND earliest_at IS NOT NULL AND latest_at IS NOT NULL
         AND earliest_at <= estimated_occurred_at
         AND estimated_occurred_at <= latest_at)
        OR
        (time_quality <> 'bounded' AND earliest_at IS NULL AND latest_at IS NULL)
    )
);

CREATE INDEX assessment_at_watermark_idx
    ON event_time_assessments
    (tenant_id, user_id, event_id, published_user_seq DESC);
