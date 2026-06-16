ALTER TABLE tenant_config
  ADD COLUMN IF NOT EXISTS notif_phone    text    NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS notif_instance text    NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS notif_enabled  boolean NOT NULL DEFAULT false;
