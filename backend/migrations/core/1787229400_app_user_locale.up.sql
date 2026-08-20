-- The language a person chose, so a machine writing for them can use it.
--
-- The i18n resolver already documents this order — user.locale, then the
-- workspace's, then the browser's guess — and said plainly that the browser is
-- the best signal it has "until /v1/me carries a locale". This is that column.
--
-- It matters beyond the interface: free text an agent writes into a record had
-- no language rule at all, so a German workspace could get English prose (or
-- the reverse) with nothing to consult. Drafts have had a language rule for a
-- while; record writes had none, because the language was not knowable.
--
-- NULL means "not chosen" and falls back, which is why there is no default: a
-- column defaulted to 'en' cannot tell a person who picked English from one
-- who never picked.
-- Bounded: an unbounded ALTER queues behind any open transaction and stalls
-- every write to app_user for as long as it is willing to wait, which is
-- forever. Three seconds, as core/0139 does and explains.
SET LOCAL lock_timeout = '3s';

ALTER TABLE app_user ADD COLUMN locale text
  CONSTRAINT app_user_locale_shipped CHECK (locale IN ('en', 'de', 'vi'));
