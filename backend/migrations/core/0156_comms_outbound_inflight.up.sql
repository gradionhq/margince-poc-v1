-- 0150: comms_outbound records that a transmission is IN FLIGHT, which is what
-- makes a channel send at-most-once (telegram-oa design §8.4).
--
-- Telegram's sendMessage carries no idempotency key and offers no prior-send
-- lookup, so nothing can ask the provider whether an earlier attempt already
-- delivered. The marker is committed BEFORE the provider call and retracted only
-- on a definite answer from it, which makes an outcome that was never learned
-- visible to the next attempt: the delivery stops with the uncertainty recorded
-- instead of messaging the customer a second time. A rare unsent message is a
-- better failure than a duplicate nobody can detect.
--
-- It is NOT an in-flight STATUS. A status would make the job runner's
-- at-least-once redelivery a silent skip for MAIL as well, disabling the
-- connector's retransmission check in exactly the crash it exists for. It is not
-- a claim either: one job per delivery is what serializes attempts, and a second
-- lock would only add a way to strand a row.
--
-- The CHECK binds it to the channel shape, because that is the shape whose
-- retries cannot self-detect. A marker on a mail row would be read by nothing
-- and would suggest a guarantee mail does not get from this column.
ALTER TABLE comms_outbound ADD COLUMN inflight_at timestamptz NULL;

ALTER TABLE comms_outbound
  ADD CONSTRAINT comms_outbound_inflight_is_channel
  CHECK (inflight_at IS NULL OR channel_user_id IS NOT NULL);
