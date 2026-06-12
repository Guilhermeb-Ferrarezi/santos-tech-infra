-- Log de processamento: um registro por mensagem inbound processada pelo engine.
CREATE TABLE IF NOT EXISTS bot_processing_log (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        text        NOT NULL,
    conversation_id  uuid        REFERENCES conversation(id) ON DELETE SET NULL,
    contact_phone    text        NOT NULL DEFAULT '',
    contact_name     text        NOT NULL DEFAULT '',
    inbound_text     text        NOT NULL DEFAULT '',
    answered         boolean,
    answered_from_kb boolean,
    handoff          boolean,
    cited_entry_ids  jsonb,
    bubbles          jsonb,
    processing_ms    integer     NOT NULL DEFAULT 0,
    error            text,
    created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS bot_processing_log_tenant_created
    ON bot_processing_log(tenant_id, created_at DESC);
