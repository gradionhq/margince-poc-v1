UPDATE role SET permissions = permissions #- '{objects,voice_profile}'
WHERE is_system AND permissions->'objects' ? 'voice_profile';
DROP TABLE voice_corpus_source;
DROP TABLE voice_profile;
