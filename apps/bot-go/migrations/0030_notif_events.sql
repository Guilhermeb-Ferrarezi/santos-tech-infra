ALTER TABLE tenant_config
  ADD COLUMN IF NOT EXISTS notif_on_success        boolean NOT NULL DEFAULT false,
  ADD COLUMN IF NOT EXISTS notif_on_container_down boolean NOT NULL DEFAULT true;
