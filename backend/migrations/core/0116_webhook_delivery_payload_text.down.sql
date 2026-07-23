ALTER TABLE webhook_delivery ALTER COLUMN payload TYPE jsonb USING payload::jsonb;
