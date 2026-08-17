-- 0282: let a capture connection name the offline demo provider.
--
-- The demo has companies, deals, contracts and invoices and an empty inbox.
-- Every other record type is seeded through the product's own API; captured
-- mail cannot be, because there is no create endpoint for it -- a message
-- arrives through the capture pipeline or it does not exist. The seeder had no
-- way in, so "who on our team knows this contact", the reply join, the thread
-- view and the person timeline all sat empty in front of anyone being shown
-- the product.
--
-- `offline_demo` is a connector that generates deterministic correspondence
-- from the records already in the database and pushes it through the REAL
-- sink, so the threads, participants and audit rows are the ones the product
-- writes rather than ones a seeder invented behind it. It has no HTTP client
-- and implements no send interface: nothing it produces can leave the machine.
--
-- The name matches the finance mirror's `offline_demo` provider, which fills
-- the revenue card the same way and for the same reason.
--
-- Widening a CHECK is additive: every existing row still satisfies it, and a
-- connection naming this provider can only exist where somebody inserted one,
-- which only the seeder and scripts/seed-dev.sql do.

ALTER TABLE capture_connection
  DROP CONSTRAINT capture_connection_provider_check;

ALTER TABLE capture_connection
  ADD CONSTRAINT capture_connection_provider_check
  CHECK (provider IN ('gmail','gcal','imap','graph','whatsapp','telegram','offline_demo'));
