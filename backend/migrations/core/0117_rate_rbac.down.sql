UPDATE role SET permissions = permissions #- '{objects,fx_rate}' WHERE is_system;
UPDATE role SET permissions = permissions #- '{objects,ai_model_rate}' WHERE is_system;
