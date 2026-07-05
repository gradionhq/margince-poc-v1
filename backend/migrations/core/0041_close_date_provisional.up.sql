-- Close-date hygiene (formulas-and-rules §11, DECISIONS A6): the nightly
-- corrector replaces an open deal's past/missing close date so
-- INV-CLOSE-PAST holds, but when the replacement is a computed guess
-- awaiting human confirmation the row is marked provisional — the
-- forecast keeps excluding it from Commit/Best-case until a human sets
-- the real date (a guessed number never moves the forecast, P12).
ALTER TABLE deal ADD COLUMN close_date_provisional boolean NOT NULL DEFAULT false;
