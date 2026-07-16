-- 0075: reconcile the route_lead / assign_lead_owner catalog split
-- (AUTO-NOTE-2, features/03 §3.5): the spec's user-reachable vocabulary
-- (AUTO-PARAM-5) reserves "Route new lead to a task" for a create_task
-- effect. The route_lead handler this codebase already shipped instead
-- ASSIGNS AN OWNER (people.LeadRoutingWorkflow) — a different act, so
-- it moves to its own honest catalog key, assign_lead_owner
-- (automations_catalog.go), and automation's own route_lead key is
-- freed for a NEW create_task handler (workflows_starter.go).
--
-- Every automation row keyed 'route_lead' before this release is,
-- unambiguously, an owner-assignment instance: the create_task reading
-- of "route_lead" did not exist until this migration ships alongside
-- it. Re-key those rows so they keep firing under the renamed handler —
-- the engine dispatches instances by exact key = Spec().Name
-- (workflows.go's HandleEvent), so an un-rekeyed row would go silently
-- dark: it would still read "Active" in the UI, never fire again, and
-- log nothing, rather than erroring loudly.
--
-- The `trigger`/`action` jsonb snapshots (data-model §12.5) do NOT need
-- to change: both catalog entries fire on the identical event
-- (lead.created → {"event_type":"lead.created"}) and the owner-
-- assignment automation still plans the identical executor
-- ({"kind":"assign_owner"}, catalog_actions.go's ActionTypeAssignOwner)
-- — only the KEY (which handler dispatch resolves against) and the
-- catalog-default display NAME move. `name` is only relabeled when it
-- still carries the pre-migration catalog default, so a human-renamed
-- instance keeps whatever the author called it.
UPDATE automation
   SET key  = 'assign_lead_owner',
       name = CASE WHEN name = 'Route new leads' THEN 'Assign new leads an owner' ELSE name END
 WHERE key = 'route_lead';
