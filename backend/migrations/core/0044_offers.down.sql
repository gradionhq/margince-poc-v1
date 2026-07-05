UPDATE role SET permissions = permissions #- '{objects,offer}' WHERE is_system;
DROP TABLE IF EXISTS offer_line_item;
DROP TABLE IF EXISTS offer;
