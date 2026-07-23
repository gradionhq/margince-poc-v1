-- Mirror of the up: the up added these objects only to admin/ops (and only
-- where absent), so the down removes them only from admin/ops. Scoping the
-- removal to the roles the up wrote keeps rollback from erasing an fx_rate /
-- ai_model_rate grant this migration did not create.
UPDATE role SET permissions = permissions #- '{objects,fx_rate}'
  WHERE is_system AND key IN ('admin','ops');
UPDATE role SET permissions = permissions #- '{objects,ai_model_rate}'
  WHERE is_system AND key IN ('admin','ops');
