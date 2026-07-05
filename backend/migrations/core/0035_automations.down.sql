UPDATE role SET permissions = permissions #- '{objects,automation}'
WHERE is_system AND permissions->'objects' ? 'automation';
DROP TABLE automation;
