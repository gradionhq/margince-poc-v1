DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = permissions #- '{objects,voice_profile}'
    WHERE (is_system AND permissions->'objects' ? 'voice_profile')
      AND role.workspace_id = ws;
  END LOOP;
END $$;

DROP TABLE voice_corpus_source;
DROP TABLE voice_profile;
