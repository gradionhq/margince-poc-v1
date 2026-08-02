-- 0165: retire the per-user personal-mail exclusion rule set (RC-2,
-- capture.md CAP-DDL-3 / CAP-WIRE-2).
--
-- The feature let each connected human keep chosen mail out of the CRM by
-- sender domain, recipient domain, or mail label, evaluated in the Sink before
-- any write. It is withdrawn: a per-user boundary on a workspace-shared record
-- set was the wrong scope, and the domain-level control that survives is the
-- workspace's own consumer-mail list (CAP-PARAM-5), which every connection in
-- the installation shares.
--
-- Shipped core migrations are additive-only, so 0076 stands and this drops what
-- it created. The rules were personal boundary settings, not records: nothing
-- references them, and no CRM row was ever derived from one.

DROP TABLE IF EXISTS capture_exclusion_rule;
