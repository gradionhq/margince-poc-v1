UPDATE role SET permissions = permissions #- '{objects,import_run}'
WHERE is_system AND permissions->'objects' ? 'import_run';
