-- 0053: audit verb vocabulary catch-up (data-model §12.5). One widening,
-- not five: `demote` (lead re-demote, formulas §26), `import`/`import_undo`
-- (A93 importer), plus three verbs the spec CHECK carried that earlier
-- widenings here missed — `disqualify`, `anonymize`, `send_email`.
-- `resolve` stays: it is live in signal_resolution writes (0047) and is
-- flagged upstream for spec adoption.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'login','assign','advance_stage','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve',
                    'demote','import','import_undo',
                    'disqualify','anonymize','send_email'));
