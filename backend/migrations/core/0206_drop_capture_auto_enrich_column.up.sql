-- The auto-enrich posture's column goes, now that nothing reads it (#521,
-- ADR-0090/A135). 0190 moved the value into `setting` and switched the one
-- reader — capture's own settings store — to it; this drops the column the
-- value used to live in.
--
-- Held back until now on purpose: 0190 kept the column so its own reverse had
-- somewhere to put the value back, and so a rollback landed on a schema whose
-- readers still worked. That obligation ends here, and 0206's own reverse
-- restores both the column and its value.
--
-- Deliberately NOT dropping name, timezone or base_currency in the same
-- change. Those still have readers — roll-ups, FX conversion, quota
-- attainment and the report builder read them directly, which is why
-- UpdateInstallation mirrors onto them — and dropping a column out from under
-- eight readers is a different change with a different risk. #521 tracks them.
-- Carry the posture forward for the population 0190 deliberately skipped.
--
-- 0190 writes the setting row only when exactly one live workspace exists,
-- because which row an operator retains is their decision and not a
-- migration's. That was safe only while the column survived to be resolved
-- FROM. Dropping it here without this would destroy the last record of an OFF
-- posture on a multi-workspace database: the operator archives the extras, the
-- API boots, the absent setting reads its registered default — true — and the
-- auto-enrich sweep resumes billable deep reads nobody asked it to resume,
-- with nothing anywhere reporting that a control flipped.
--
-- So: if any live workspace had it OFF and no setting row exists yet, write
-- OFF. That can be wrong in one direction — an installation whose retained
-- workspace had it ON inherits OFF from a sibling — and that is the direction
-- to be wrong in. This is a spend control; the recoverable mistake is the one
-- an operator fixes with a toggle, not the one they find on an invoice.
INSERT INTO setting (key, value)
SELECT 'capture.auto_enrich', to_jsonb(false)
 WHERE EXISTS (
   SELECT 1 FROM workspace
    WHERE archived_at IS NULL AND capture_auto_enrich = false
 )
    ON CONFLICT (key) DO NOTHING;

ALTER TABLE workspace DROP COLUMN capture_auto_enrich;
