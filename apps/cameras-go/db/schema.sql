CREATE TABLE cameras (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    ip TEXT NOT NULL,
    rtsp_user TEXT NOT NULL,
    rtsp_password_encrypted TEXT NOT NULL,
    quality_main TEXT NOT NULL,
    quality_sub TEXT NOT NULL,
    record_mode TEXT NOT NULL,
    motion_sensitivity INT NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE recordings (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    camera_id UUID NOT NULL REFERENCES cameras(id) ON DELETE CASCADE,
    start_time TIMESTAMPTZ NOT NULL,
    end_time TIMESTAMPTZ NOT NULL,
    size_bytes BIGINT NOT NULL,
    drive_file_id TEXT NOT NULL,
    has_motion BOOLEAN NOT NULL DEFAULT false,
    keep_forever BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE settings (
    id INT PRIMARY KEY DEFAULT 1,
    storage_quota_bytes BIGINT NOT NULL DEFAULT 4947802324992, -- 4.5 TB
    drive_refresh_token_encrypted TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
