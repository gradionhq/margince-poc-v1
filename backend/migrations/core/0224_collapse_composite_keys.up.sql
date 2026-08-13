-- ADR-0091 §8 phase B: the composite keys collapse to what is left of them.
--
-- Every PRIMARY KEY and UNIQUE that carried `workspace_id` loses it. This runs
-- AFTER phase C (0218), which §8's own rule requires: B must precede C *for the
-- referenced table*, and that referenced key — each entity table's `id` PRIMARY
-- KEY — already existed. What was left of B is this DROP half, and it could not
-- run while foreign keys still pointed at the composite uniques.
--
-- Why the remainder is still a key. Every one of these was unique WITHIN a
-- workspace; an installation holds one workspace (ADR-0061/A107), so the
-- remaining columns are unique across the whole table by construction. It is
-- the ADR's own argument applied to keys: a discriminator with one value
-- discriminates nothing.
--
-- Read out of the catalog rather than retyped — partial predicates, INCLUDE
-- lists and NULLS NOT DISTINCT clauses come back verbatim.
--
-- Three groups are deferred to 20260813130000_collapse_composite_keys_custom,
-- which runs after the whole custom chain: that namespace's own keys, the
-- three tables keyed on the tenant ALONE (all of them its), and `app_user`'s
-- composite unique — which core cannot drop, because a historical custom
-- migration still creates a foreign key against it. Dropping it here makes the
-- overlay cluster's own creation migration fail on a fresh database, which is
-- exactly the layering §8 warns about.

ALTER TABLE activity DROP CONSTRAINT uq_activity_ws_id;
ALTER TABLE activity ADD CONSTRAINT uq_activity_ws_id UNIQUE (id);

ALTER TABLE activity_participant_replay DROP CONSTRAINT activity_participant_replay_pkey;
ALTER TABLE activity_participant_replay ADD CONSTRAINT activity_participant_replay_pkey PRIMARY KEY (activity_id);

ALTER TABLE agent_run DROP CONSTRAINT agent_run_trigger_unique;
ALTER TABLE agent_run ADD CONSTRAINT agent_run_trigger_unique UNIQUE (trigger_ref);

ALTER TABLE agent_run DROP CONSTRAINT uq_agent_run_ws_id;
ALTER TABLE agent_run ADD CONSTRAINT uq_agent_run_ws_id UNIQUE (id);

ALTER TABLE ai_call DROP CONSTRAINT uq_ai_call_ws_id;
ALTER TABLE ai_call ADD CONSTRAINT uq_ai_call_ws_id UNIQUE (id);

ALTER TABLE ai_feedback DROP CONSTRAINT ai_feedback_workspace_id_subject_type_subject_id_claim_kind_key;
ALTER TABLE ai_feedback ADD CONSTRAINT ai_feedback_workspace_id_subject_type_subject_id_claim_kind_key UNIQUE (subject_type, subject_id, claim_kind, claim_key);

ALTER TABLE ai_model_rate DROP CONSTRAINT ai_model_rate_key;
ALTER TABLE ai_model_rate ADD CONSTRAINT ai_model_rate_key UNIQUE (provider, model_id, effective_date);

ALTER TABLE ai_usage DROP CONSTRAINT ai_usage_pkey;
ALTER TABLE ai_usage ADD CONSTRAINT ai_usage_pkey PRIMARY KEY (day, task, tier);

ALTER TABLE approval DROP CONSTRAINT uq_approval_ws_id;
ALTER TABLE approval ADD CONSTRAINT uq_approval_ws_id UNIQUE (id);

ALTER TABLE attachment DROP CONSTRAINT uq_attachment_ws_id;
ALTER TABLE attachment ADD CONSTRAINT uq_attachment_ws_id UNIQUE (id);

ALTER TABLE automation DROP CONSTRAINT uq_automation_ws_id;
ALTER TABLE automation ADD CONSTRAINT uq_automation_ws_id UNIQUE (id);

ALTER TABLE brief_run DROP CONSTRAINT uq_brief_run_ws_id;
ALTER TABLE brief_run ADD CONSTRAINT uq_brief_run_ws_id UNIQUE (id);

ALTER TABLE capture_auto_enrich_budget DROP CONSTRAINT capture_auto_enrich_budget_pkey;
ALTER TABLE capture_auto_enrich_budget ADD CONSTRAINT capture_auto_enrich_budget_pkey PRIMARY KEY (budget_date);

ALTER TABLE capture_connection DROP CONSTRAINT capture_connection_unique;
ALTER TABLE capture_connection ADD CONSTRAINT capture_connection_unique UNIQUE (user_id, provider);

ALTER TABLE capture_connection DROP CONSTRAINT uq_capture_connection_ws_id;
ALTER TABLE capture_connection ADD CONSTRAINT uq_capture_connection_ws_id UNIQUE (id);

ALTER TABLE capture_digest DROP CONSTRAINT capture_digest_workspace_id_user_id_digest_date_key;
ALTER TABLE capture_digest ADD CONSTRAINT capture_digest_workspace_id_user_id_digest_date_key UNIQUE (user_id, digest_date);

ALTER TABLE comms_outbound DROP CONSTRAINT comms_outbound_message_unique;
ALTER TABLE comms_outbound ADD CONSTRAINT comms_outbound_message_unique UNIQUE (message_id);

ALTER TABLE consent_doi_token DROP CONSTRAINT consent_doi_token_hash_unique;
ALTER TABLE consent_doi_token ADD CONSTRAINT consent_doi_token_hash_unique UNIQUE (token_hash);

ALTER TABLE consent_existing_customer_flag DROP CONSTRAINT consent_existing_customer_flag_pkey;
ALTER TABLE consent_existing_customer_flag ADD CONSTRAINT consent_existing_customer_flag_pkey PRIMARY KEY (person_id);

ALTER TABLE consent_purpose DROP CONSTRAINT consent_purpose_key_unique;
ALTER TABLE consent_purpose ADD CONSTRAINT consent_purpose_key_unique UNIQUE (key);

ALTER TABLE consent_purpose DROP CONSTRAINT uq_consent_purpose_ws_id;
ALTER TABLE consent_purpose ADD CONSTRAINT uq_consent_purpose_ws_id UNIQUE (id);

ALTER TABLE deal DROP CONSTRAINT uq_deal_ws_id;
ALTER TABLE deal ADD CONSTRAINT uq_deal_ws_id UNIQUE (id);

ALTER TABLE embedding DROP CONSTRAINT embedding_pkey;
ALTER TABLE embedding ADD CONSTRAINT embedding_pkey PRIMARY KEY (entity_type, entity_id, chunk_ix);

ALTER TABLE erasure_suppression DROP CONSTRAINT erasure_suppression_pkey;
ALTER TABLE erasure_suppression ADD CONSTRAINT erasure_suppression_pkey PRIMARY KEY (kind, value_hash);

ALTER TABLE finance_connection DROP CONSTRAINT uq_finance_connection_ws_id;
ALTER TABLE finance_connection ADD CONSTRAINT uq_finance_connection_ws_id UNIQUE (id);

ALTER TABLE finance_external_customer DROP CONSTRAINT finance_external_customer_workspace_id_connection_id_extern_key;
ALTER TABLE finance_external_customer ADD CONSTRAINT finance_external_customer_workspace_id_connection_id_extern_key UNIQUE (connection_id, external_customer_id);

ALTER TABLE finance_invoice DROP CONSTRAINT finance_invoice_workspace_id_connection_id_external_id_key;
ALTER TABLE finance_invoice ADD CONSTRAINT finance_invoice_workspace_id_connection_id_external_id_key UNIQUE (connection_id, external_id);

ALTER TABLE finance_invoice DROP CONSTRAINT uq_finance_invoice_ws_id;
ALTER TABLE finance_invoice ADD CONSTRAINT uq_finance_invoice_ws_id UNIQUE (id);

ALTER TABLE finance_payment DROP CONSTRAINT finance_payment_workspace_id_connection_id_external_id_key;
ALTER TABLE finance_payment ADD CONSTRAINT finance_payment_workspace_id_connection_id_external_id_key UNIQUE (connection_id, external_id);

ALTER TABLE fx_rate DROP CONSTRAINT fx_rate_pair_day;
ALTER TABLE fx_rate ADD CONSTRAINT fx_rate_pair_day UNIQUE (from_currency, to_currency, rate_date);

ALTER TABLE graph_interaction_edge DROP CONSTRAINT graph_interaction_edge_pkey;
ALTER TABLE graph_interaction_edge ADD CONSTRAINT graph_interaction_edge_pkey PRIMARY KEY (user_id, person_id);

ALTER TABLE idempotency_key DROP CONSTRAINT idempotency_key_pkey;
ALTER TABLE idempotency_key ADD CONSTRAINT idempotency_key_pkey PRIMARY KEY (principal_id, key, endpoint);

ALTER TABLE lead DROP CONSTRAINT uq_lead_ws_id;
ALTER TABLE lead ADD CONSTRAINT uq_lead_ws_id UNIQUE (id);

ALTER TABLE linkedin_account DROP CONSTRAINT linkedin_account_pkey;
ALTER TABLE linkedin_account ADD CONSTRAINT linkedin_account_pkey PRIMARY KEY (user_id);

ALTER TABLE list DROP CONSTRAINT uq_list_ws_id;
ALTER TABLE list ADD CONSTRAINT uq_list_ws_id UNIQUE (id);

ALTER TABLE oauth_authorization_code DROP CONSTRAINT oauth_code_unique;
ALTER TABLE oauth_authorization_code ADD CONSTRAINT oauth_code_unique UNIQUE (code_hash);

ALTER TABLE oauth_client DROP CONSTRAINT oauth_client_unique;
ALTER TABLE oauth_client ADD CONSTRAINT oauth_client_unique UNIQUE (client_id);

ALTER TABLE oauth_grant DROP CONSTRAINT oauth_grant_ws_id_key;
ALTER TABLE oauth_grant ADD CONSTRAINT oauth_grant_ws_id_key UNIQUE (id);

ALTER TABLE oauth_refresh_token DROP CONSTRAINT oauth_refresh_unique;
ALTER TABLE oauth_refresh_token ADD CONSTRAINT oauth_refresh_unique UNIQUE (token_hash);

ALTER TABLE offer DROP CONSTRAINT offer_number_rev_unique;
ALTER TABLE offer ADD CONSTRAINT offer_number_rev_unique UNIQUE (offer_number, revision);

ALTER TABLE offer DROP CONSTRAINT uq_offer_ws_id;
ALTER TABLE offer ADD CONSTRAINT uq_offer_ws_id UNIQUE (id);

ALTER TABLE offer_template DROP CONSTRAINT offer_template_name_unique;
ALTER TABLE offer_template ADD CONSTRAINT offer_template_name_unique UNIQUE (name);

ALTER TABLE offer_template DROP CONSTRAINT uq_offer_template_ws_id;
ALTER TABLE offer_template ADD CONSTRAINT uq_offer_template_ws_id UNIQUE (id);

ALTER TABLE onboarding_wizard_state DROP CONSTRAINT onboarding_wizard_state_workspace_id_user_id_key;
ALTER TABLE onboarding_wizard_state ADD CONSTRAINT onboarding_wizard_state_workspace_id_user_id_key UNIQUE (user_id);

ALTER TABLE org_brief DROP CONSTRAINT org_brief_workspace_id_user_id_organization_id_key;
ALTER TABLE org_brief ADD CONSTRAINT org_brief_workspace_id_user_id_organization_id_key UNIQUE (user_id, organization_id);

ALTER TABLE org_dossier DROP CONSTRAINT org_dossier_pkey;
ALTER TABLE org_dossier ADD CONSTRAINT org_dossier_pkey PRIMARY KEY (user_id, organization_id);

ALTER TABLE org_growth_fit DROP CONSTRAINT org_growth_fit_pkey;
ALTER TABLE org_growth_fit ADD CONSTRAINT org_growth_fit_pkey PRIMARY KEY (user_id, organization_id);

ALTER TABLE organization DROP CONSTRAINT uq_organization_ws_id;
ALTER TABLE organization ADD CONSTRAINT uq_organization_ws_id UNIQUE (id);

ALTER TABLE organization_fact DROP CONSTRAINT uq_org_fact;
ALTER TABLE organization_fact ADD CONSTRAINT uq_org_fact UNIQUE (organization_id, category, field, value_key);

ALTER TABLE organization_profile_field DROP CONSTRAINT uq_org_profile_field;
ALTER TABLE organization_profile_field ADD CONSTRAINT uq_org_profile_field UNIQUE (organization_id, field);

ALTER TABLE passport DROP CONSTRAINT uq_passport_ws_id;
ALTER TABLE passport ADD CONSTRAINT uq_passport_ws_id UNIQUE (id);

ALTER TABLE person DROP CONSTRAINT uq_person_ws_id;
ALTER TABLE person ADD CONSTRAINT uq_person_ws_id UNIQUE (id);

ALTER TABLE person_brief DROP CONSTRAINT person_brief_pkey;
ALTER TABLE person_brief ADD CONSTRAINT person_brief_pkey PRIMARY KEY (user_id, person_id);

ALTER TABLE person_consent DROP CONSTRAINT person_consent_lead_unique;
ALTER TABLE person_consent ADD CONSTRAINT person_consent_lead_unique UNIQUE (lead_id, purpose_id);

ALTER TABLE person_consent DROP CONSTRAINT person_consent_unique;
ALTER TABLE person_consent ADD CONSTRAINT person_consent_unique UNIQUE (person_id, purpose_id);

ALTER TABLE person_moment_dismissal DROP CONSTRAINT person_moment_dismissal_pkey;
ALTER TABLE person_moment_dismissal ADD CONSTRAINT person_moment_dismissal_pkey PRIMARY KEY (user_id, person_id, claim_key);

ALTER TABLE person_social DROP CONSTRAINT person_social_workspace_id_person_id_platform_key;
ALTER TABLE person_social ADD CONSTRAINT person_social_workspace_id_person_id_platform_key UNIQUE (person_id, platform);

ALTER TABLE pipeline DROP CONSTRAINT pipeline_name_unique;
ALTER TABLE pipeline ADD CONSTRAINT pipeline_name_unique UNIQUE (name);

ALTER TABLE pipeline DROP CONSTRAINT uq_pipeline_ws_id;
ALTER TABLE pipeline ADD CONSTRAINT uq_pipeline_ws_id UNIQUE (id);

ALTER TABLE product DROP CONSTRAINT uq_product_ws_id;
ALTER TABLE product ADD CONSTRAINT uq_product_ws_id UNIQUE (id);

ALTER TABLE project DROP CONSTRAINT uq_project_ws_id;
ALTER TABLE project ADD CONSTRAINT uq_project_ws_id UNIQUE (id);

ALTER TABLE raw_capture DROP CONSTRAINT raw_capture_source_unique;
ALTER TABLE raw_capture ADD CONSTRAINT raw_capture_source_unique UNIQUE (source_system, source_id);

ALTER TABLE record_grant DROP CONSTRAINT record_grant_unique;
ALTER TABLE record_grant ADD CONSTRAINT record_grant_unique UNIQUE (record_type, record_id, subject_type, subject_id);

-- NULLS NOT DISTINCT carries across the collapse: `category` is nullable and
-- DM-SEED-2 plants an ('activity', NULL) row, so without it 0223's "one row
-- per scope" reverts to "any number of NULL-category rows" — the property
-- privacy.MaxPassDuration derives its bound from.
ALTER TABLE retention_policy DROP CONSTRAINT retention_policy_unique;
ALTER TABLE retention_policy ADD CONSTRAINT retention_policy_unique
  UNIQUE NULLS NOT DISTINCT (object_type, category);

ALTER TABLE role DROP CONSTRAINT role_key_unique;
ALTER TABLE role ADD CONSTRAINT role_key_unique UNIQUE (key);

ALTER TABLE role DROP CONSTRAINT uq_role_ws_id;
ALTER TABLE role ADD CONSTRAINT uq_role_ws_id UNIQUE (id);

ALTER TABLE runner_job DROP CONSTRAINT runner_job_trigger_unique;
ALTER TABLE runner_job ADD CONSTRAINT runner_job_trigger_unique UNIQUE (agent_spec, trigger_ref);

ALTER TABLE signal DROP CONSTRAINT uq_signal_ws_id;
ALTER TABLE signal ADD CONSTRAINT uq_signal_ws_id UNIQUE (id);

ALTER TABLE signal_thread_scan DROP CONSTRAINT signal_thread_scan_pkey;
ALTER TABLE signal_thread_scan ADD CONSTRAINT signal_thread_scan_pkey PRIMARY KEY (thread_key);

ALTER TABLE site_read DROP CONSTRAINT uq_site_read_ws_id;
ALTER TABLE site_read ADD CONSTRAINT uq_site_read_ws_id UNIQUE (id);

ALTER TABLE stage DROP CONSTRAINT uq_stage_ws_id;
ALTER TABLE stage ADD CONSTRAINT uq_stage_ws_id UNIQUE (id);

ALTER TABLE stage DROP CONSTRAINT uq_stage_ws_id_pipeline;
ALTER TABLE stage ADD CONSTRAINT uq_stage_ws_id_pipeline UNIQUE (id, pipeline_id);

ALTER TABLE suggestion_dismissal DROP CONSTRAINT suggestion_dismissal_workspace_id_user_id_organization_id_f_key;
ALTER TABLE suggestion_dismissal ADD CONSTRAINT suggestion_dismissal_workspace_id_user_id_organization_id_f_key UNIQUE (user_id, organization_id, fingerprint);

ALTER TABLE tag DROP CONSTRAINT uq_tag_ws_id;
ALTER TABLE tag ADD CONSTRAINT uq_tag_ws_id UNIQUE (id);

ALTER TABLE team DROP CONSTRAINT team_name_unique;
ALTER TABLE team ADD CONSTRAINT team_name_unique UNIQUE (name);

ALTER TABLE team DROP CONSTRAINT uq_team_ws_id;
ALTER TABLE team ADD CONSTRAINT uq_team_ws_id UNIQUE (id);

ALTER TABLE user_record_view DROP CONSTRAINT user_record_view_workspace_id_user_id_entity_type_entity_id_key;
ALTER TABLE user_record_view ADD CONSTRAINT user_record_view_workspace_id_user_id_entity_type_entity_id_key UNIQUE (user_id, entity_type, entity_id);

ALTER TABLE voice_build DROP CONSTRAINT uq_voice_build_ws_id;
ALTER TABLE voice_build ADD CONSTRAINT uq_voice_build_ws_id UNIQUE (id);

ALTER TABLE voice_corpus_source DROP CONSTRAINT uq_voice_corpus_source_ref;
ALTER TABLE voice_corpus_source ADD CONSTRAINT uq_voice_corpus_source_ref UNIQUE (voice_profile_id, source_ref);

ALTER TABLE voice_learning_signal DROP CONSTRAINT uq_voice_learning_signal_draft;
ALTER TABLE voice_learning_signal ADD CONSTRAINT uq_voice_learning_signal_draft UNIQUE (draft_ref_hash);

ALTER TABLE voice_learning_signal DROP CONSTRAINT uq_voice_learning_signal_ws_id;
ALTER TABLE voice_learning_signal ADD CONSTRAINT uq_voice_learning_signal_ws_id UNIQUE (id);

ALTER TABLE voice_profile DROP CONSTRAINT uq_voice_profile_ws_id;
ALTER TABLE voice_profile ADD CONSTRAINT uq_voice_profile_ws_id UNIQUE (id);

ALTER TABLE voice_profile_delta DROP CONSTRAINT uq_voice_profile_delta_version;
ALTER TABLE voice_profile_delta ADD CONSTRAINT uq_voice_profile_delta_version UNIQUE (voice_profile_id, to_version);

ALTER TABLE voice_profile_delta DROP CONSTRAINT uq_voice_profile_delta_ws_id;
ALTER TABLE voice_profile_delta ADD CONSTRAINT uq_voice_profile_delta_ws_id UNIQUE (id);

ALTER TABLE voice_profile_version DROP CONSTRAINT uq_voice_profile_version_number;
ALTER TABLE voice_profile_version ADD CONSTRAINT uq_voice_profile_version_number UNIQUE (voice_profile_id, profile_version);

ALTER TABLE voice_profile_version DROP CONSTRAINT uq_voice_profile_version_ws_id;
ALTER TABLE voice_profile_version ADD CONSTRAINT uq_voice_profile_version_ws_id UNIQUE (id);

ALTER TABLE webhook_delivery DROP CONSTRAINT webhook_delivery_dedupe_key;
ALTER TABLE webhook_delivery ADD CONSTRAINT webhook_delivery_dedupe_key UNIQUE (subscription_id, event_id);

ALTER TABLE webhook_subscription DROP CONSTRAINT webhook_subscription_ws_id_key;
ALTER TABLE webhook_subscription ADD CONSTRAINT webhook_subscription_ws_id_key UNIQUE (id);

ALTER TABLE workflow_run DROP CONSTRAINT workflow_run_unique;
ALTER TABLE workflow_run ADD CONSTRAINT workflow_run_unique UNIQUE (handler, idempotency_key);

ALTER TABLE workspace_email_domain DROP CONSTRAINT workspace_email_domain_pkey;
ALTER TABLE workspace_email_domain ADD CONSTRAINT workspace_email_domain_pkey PRIMARY KEY (domain);

ALTER TABLE workspace_signing_key DROP CONSTRAINT workspace_signing_key_pkey;
ALTER TABLE workspace_signing_key ADD CONSTRAINT workspace_signing_key_pkey PRIMARY KEY (kid);

DROP INDEX uq_activity_source;
CREATE UNIQUE INDEX uq_activity_source ON public.activity USING btree (source_system, source_id) WHERE ((source_system IS NOT NULL) AND (source_id IS NOT NULL));

DROP INDEX idx_agent_task_approval;
CREATE UNIQUE INDEX idx_agent_task_approval ON public.agent_task USING btree (approval_id);

DROP INDEX uq_app_user_email;
CREATE UNIQUE INDEX uq_app_user_email ON public.app_user USING btree (lower(email));

DROP INDEX attachment_external_part_key;
CREATE UNIQUE INDEX attachment_external_part_key ON public.attachment USING btree (external_source_id, external_part_id) WHERE (external_source_id IS NOT NULL);

DROP INDEX uq_capture_freemail_domain;
CREATE UNIQUE INDEX uq_capture_freemail_domain ON public.capture_freemail_domain USING btree (domain);

DROP INDEX idx_capture_pending_counterparty_live;
CREATE UNIQUE INDEX idx_capture_pending_counterparty_live ON public.capture_pending_counterparty USING btree (email) WHERE (status = ANY (ARRAY['pending'::text, 'unsure'::text]));

DROP INDEX idx_capture_pending_counterparty_suppressed;
CREATE UNIQUE INDEX idx_capture_pending_counterparty_suppressed ON public.capture_pending_counterparty USING btree (email) WHERE (status = 'suppressed'::text);

DROP INDEX uq_channel_connection_ws;
CREATE UNIQUE INDEX uq_channel_connection_ws ON public.channel_connection USING btree (provider) WHERE (archived_at IS NULL);

DROP INDEX consent_qualifying_event_source_unique;
CREATE UNIQUE INDEX consent_qualifying_event_source_unique ON public.consent_qualifying_event USING btree (person_id, source_entity_type, source_entity_id) WHERE (source_entity_id IS NOT NULL);

DROP INDEX uq_custom_field_column;
CREATE UNIQUE INDEX uq_custom_field_column ON public.custom_field USING btree (object, column_name);

DROP INDEX uq_custom_field_slug;
CREATE UNIQUE INDEX uq_custom_field_slug ON public.custom_field USING btree (object, slug);

DROP INDEX uq_dedupe_candidate_pair;
CREATE UNIQUE INDEX uq_dedupe_candidate_pair ON public.dedupe_candidate USING btree (entity_type, COALESCE(left_person_id, left_org_id), COALESCE(right_person_id, right_org_id));

DROP INDEX extension_secret_user_key;
CREATE UNIQUE INDEX extension_secret_user_key ON public.extension_secret USING btree (extension_name, user_id, key) WHERE (user_id IS NOT NULL);

DROP INDEX extension_secret_workspace_key;
CREATE UNIQUE INDEX extension_secret_workspace_key ON public.extension_secret USING btree (extension_name, key) WHERE (user_id IS NULL);

DROP INDEX finance_customer_link_external_ux;
CREATE UNIQUE INDEX finance_customer_link_external_ux ON public.finance_customer_link USING btree (connection_id, external_customer_id) WHERE (archived_at IS NULL);

DROP INDEX finance_customer_link_organization_ux;
CREATE UNIQUE INDEX finance_customer_link_organization_ux ON public.finance_customer_link USING btree (connection_id, organization_id) WHERE (archived_at IS NULL);

DROP INDEX uq_lead_email_dedupe;
CREATE UNIQUE INDEX uq_lead_email_dedupe ON public.lead USING btree (email) WHERE ((email IS NOT NULL) AND (archived_at IS NULL));

DROP INDEX uq_lead_source;
CREATE UNIQUE INDEX uq_lead_source ON public.lead USING btree (source_system, source_id) WHERE ((source_system IS NOT NULL) AND (source_id IS NOT NULL));

DROP INDEX uq_linkedin_connection_natural;
CREATE UNIQUE INDEX uq_linkedin_connection_natural ON public.linkedin_connection USING btree (owner_user_id, normalized_name, COALESCE(normalized_company, ''::text), COALESCE(connected_on, '1970-01-01'::date)) WHERE (provider_member_ref IS NULL);

DROP INDEX uq_linkedin_connection_provider;
CREATE UNIQUE INDEX uq_linkedin_connection_provider ON public.linkedin_connection USING btree (owner_user_id, provider_member_ref) WHERE (provider_member_ref IS NOT NULL);

DROP INDEX uq_offer_template_default;
CREATE UNIQUE INDEX uq_offer_template_default ON public.offer_template USING btree (locale) WHERE (is_default AND (archived_at IS NULL));

DROP INDEX organization_linkedin_url_key;
CREATE UNIQUE INDEX organization_linkedin_url_key ON public.organization USING btree (lower(linkedin_url)) WHERE ((linkedin_url IS NOT NULL) AND (archived_at IS NULL));

DROP INDEX uq_org_domain;
CREATE UNIQUE INDEX uq_org_domain ON public.organization_domain USING btree (domain) WHERE (archived_at IS NULL);

DROP INDEX uq_organization_domain_disposition;
CREATE UNIQUE INDEX uq_organization_domain_disposition ON public.organization_domain_disposition USING btree (domain);

DROP INDEX uq_person_channel_identity;
CREATE UNIQUE INDEX uq_person_channel_identity ON public.person_channel_identity USING btree (provider, channel_user_id) WHERE (archived_at IS NULL);

DROP INDEX uq_person_email_dedupe;
CREATE UNIQUE INDEX uq_person_email_dedupe ON public.person_email USING btree (email) WHERE (archived_at IS NULL);

DROP INDEX uq_preference_token_person;
CREATE UNIQUE INDEX uq_preference_token_person ON public.preference_token USING btree (person_id) WHERE (revoked_at IS NULL);

DROP INDEX uq_product_sku;
CREATE UNIQUE INDEX uq_product_sku ON public.product USING btree (sku) WHERE ((sku IS NOT NULL) AND (archived_at IS NULL));

DROP INDEX uq_project_key;
CREATE UNIQUE INDEX uq_project_key ON public.project USING btree (lower(key)) WHERE ((key IS NOT NULL) AND (archived_at IS NULL));

DROP INDEX uq_signal_fingerprint;
CREATE UNIQUE INDEX uq_signal_fingerprint ON public.signal USING btree (fingerprint) WHERE ((fingerprint IS NOT NULL) AND (archived_at IS NULL));

DROP INDEX uq_site_read_onboarding_inflight;
CREATE UNIQUE INDEX uq_site_read_onboarding_inflight ON public.site_read USING btree (seed_url) WHERE ((target_kind = 'onboarding'::text) AND (status = ANY (ARRAY['queued'::text, 'deferred'::text, 'running'::text])));

DROP INDEX uq_site_read_org_inflight;
CREATE UNIQUE INDEX uq_site_read_org_inflight ON public.site_read USING btree (organization_id, seed_url) WHERE ((target_kind = 'organization'::text) AND (status = ANY (ARRAY['queued'::text, 'deferred'::text, 'running'::text])));

DROP INDEX uq_site_read_triage_inflight;
CREATE UNIQUE INDEX uq_site_read_triage_inflight ON public.site_read USING btree (seed_url) WHERE ((target_kind = 'domain_triage'::text) AND (status = ANY (ARRAY['queued'::text, 'deferred'::text, 'running'::text])));

DROP INDEX uq_tag_name;
CREATE UNIQUE INDEX uq_tag_name ON public.tag USING btree (lower(name));

DROP INDEX voice_build_one_active;
CREATE UNIQUE INDEX voice_build_one_active ON public.voice_build USING btree (voice_profile_id) WHERE (status = ANY (ARRAY['queued'::text, 'deferred'::text, 'running'::text]));

DROP INDEX uq_voice_profile_user_live;
CREATE UNIQUE INDEX uq_voice_profile_user_live ON public.voice_profile USING btree (owner_id) WHERE ((scope = 'user'::text) AND (archived_at IS NULL));

DROP INDEX voice_profile_version_one_active;
CREATE UNIQUE INDEX voice_profile_version_one_active ON public.voice_profile_version USING btree (voice_profile_id) WHERE (status = 'active'::text);

-- Two partial indexes were keyed on the tenant ALONE — one default pipeline,
-- one anchor organization, per workspace. Dropping the key would not collapse
-- them, it would let each hold many rows where it held one. The key becomes a
-- constant expression, the standard spelling of "at most one row": the index
-- still has something to be unique on, and the predicates carry over unchanged.
DROP INDEX uq_pipeline_default;
CREATE UNIQUE INDEX uq_pipeline_default ON pipeline ((true)) WHERE (is_default AND archived_at IS NULL);

DROP INDEX uq_organization_anchor;
CREATE UNIQUE INDEX uq_organization_anchor ON organization ((true)) WHERE (is_anchor AND archived_at IS NULL);

-- Names that no longer describe their key. A constraint keeps its NAME when its
-- columns change, so several still announce a `workspace_id` they no longer
-- contain. A name that lies is worse than a long one: the next reader has to
-- open the catalog to find out which is true.
ALTER INDEX ai_feedback_workspace_id_subject_type_subject_id_claim_kind_key RENAME TO ai_feedback_subject_type_subject_id_claim_kind_key;
ALTER INDEX capture_digest_workspace_id_user_id_digest_date_key RENAME TO capture_digest_user_id_digest_date_key;
ALTER INDEX finance_external_customer_workspace_id_connection_id_extern_key RENAME TO finance_external_customer_connection_id_extern_key;
ALTER INDEX finance_invoice_workspace_id_connection_id_external_id_key RENAME TO finance_invoice_connection_id_external_id_key;
ALTER INDEX finance_payment_workspace_id_connection_id_external_id_key RENAME TO finance_payment_connection_id_external_id_key;
ALTER INDEX onboarding_wizard_state_workspace_id_user_id_key RENAME TO onboarding_wizard_state_user_id_key;
ALTER INDEX org_brief_workspace_id_user_id_organization_id_key RENAME TO org_brief_user_id_organization_id_key;
ALTER INDEX person_social_workspace_id_person_id_platform_key RENAME TO person_social_person_id_platform_key;
ALTER INDEX suggestion_dismissal_workspace_id_user_id_organization_id_f_key RENAME TO suggestion_dismissal_user_id_organization_id_f_key;
ALTER INDEX user_record_view_workspace_id_user_id_entity_type_entity_id_key RENAME TO user_record_view_user_id_entity_type_entity_id_key;
