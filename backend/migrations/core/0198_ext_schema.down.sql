-- Reverses 0198. RESTRICT, never CASCADE: if any extension table still lives
-- in ext, reverting the core lane must FAIL loudly rather than silently
-- destroy an installed unit's data — an extension's own migrations are a
-- separate ownership domain and revert on their own terms.
DROP SCHEMA IF EXISTS ext RESTRICT;
