UPDATE role SET permissions = permissions #- '{objects,product}' WHERE is_system;
DROP TABLE IF EXISTS product;
