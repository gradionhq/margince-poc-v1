ALTER TABLE person
  ADD COLUMN social  jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN address jsonb NULL;
ALTER TABLE organization
  ADD COLUMN address jsonb NULL;

DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE person p SET social = coalesce(
      (SELECT jsonb_object_agg(ps.platform, ps.handle) FROM person_social ps WHERE ps.person_id = p.id),
      '{}'::jsonb)
    WHERE p.workspace_id = ws;

    UPDATE person SET address = jsonb_strip_nulls(jsonb_build_object(
      'line1', address_line1, 'line2', address_line2, 'city', address_city,
      'region', address_region, 'postal_code', address_postal_code, 'country', address_country))
    WHERE (address_line1 IS NOT NULL OR address_line2 IS NOT NULL OR address_city IS NOT NULL
       OR address_region IS NOT NULL OR address_postal_code IS NOT NULL OR address_country IS NOT NULL)
      AND person.workspace_id = ws;

    UPDATE organization SET address = jsonb_strip_nulls(jsonb_build_object(
      'line1', address_line1, 'line2', address_line2, 'city', address_city,
      'region', address_region, 'postal_code', address_postal_code, 'country', address_country))
    WHERE (address_line1 IS NOT NULL OR address_line2 IS NOT NULL OR address_city IS NOT NULL
       OR address_region IS NOT NULL OR address_postal_code IS NOT NULL OR address_country IS NOT NULL)
      AND organization.workspace_id = ws;
  END LOOP;
END $$;

ALTER TABLE person DROP COLUMN address_line1, DROP COLUMN address_line2, DROP COLUMN address_city,
  DROP COLUMN address_region, DROP COLUMN address_postal_code, DROP COLUMN address_country;
ALTER TABLE organization DROP COLUMN address_line1, DROP COLUMN address_line2, DROP COLUMN address_city,
  DROP COLUMN address_region, DROP COLUMN address_postal_code, DROP COLUMN address_country;

DROP TABLE person_social;
