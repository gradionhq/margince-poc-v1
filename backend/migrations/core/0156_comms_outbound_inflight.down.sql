-- 0150 rollback: the column and its shape constraint go together. The marker is
-- the ONLY record that a channel message may already be with the customer, and a
-- channel seam has no prior-send lookup to reconstruct it from — so the rows
-- standing on it are closed before it is dropped.
--
-- Three states hold the marker while the row is still pending: the provider call
-- itself, a successful send whose receipt write failed, and a worker killed
-- mid-transmission. Dropping the marker under any of them hands the delivery
-- back to the runner looking untried, and the customer gets a second copy with
-- nothing able to notice. `migrate down` reverts newest-first, one step by
-- default, so 0150 comes off ALONE: 0149's blanket DELETE of the channel rows
-- does not run and these rows survive to be redelivered.
--
-- dbmigrate.Down runs this file in ONE transaction, so the park and the drop are
-- atomic — there is no window in which the rows are unparked and the evidence is
-- already gone.
--
-- The sentence is a COPY of comms.unknownOutcomeReason, not a reference: SQL
-- cannot read a Go constant, and the operator has to be told the same thing here
-- as the send path tells them, because it is the same fact about the same row.
UPDATE comms_outbound
   SET status = 'parked',
       reason = 'the provider never confirmed whether this message was delivered, '
             || 'and it will not be retried: a second attempt could deliver it twice with nothing able to tell. '
             || 'Check the conversation and send again if it did not arrive'
 WHERE status = 'pending' AND inflight_at IS NOT NULL;

ALTER TABLE comms_outbound DROP CONSTRAINT comms_outbound_inflight_is_channel;

ALTER TABLE comms_outbound DROP COLUMN inflight_at;
