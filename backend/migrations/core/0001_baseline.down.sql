-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion

-- Reverses the baseline, in the mirror of the order it builds: the constraints,
-- indexes, triggers, grants and reference rows first, then the tables, then the
-- functions their defaults and triggers referenced, then the extensions and the
-- `ext` schema.
--
-- Spelled out object by object rather than as one DROP SCHEMA, because
-- `migrate down` reverts a migration and not a database: the schema has to come
-- back the same way a fresh apply would build it, which the round trip in
-- schema_integration_test.go asserts by re-applying afterwards.
--
-- `ext` is dropped WITHOUT cascade. It holds extension units' tables, which
-- belong to another namespace's ledger, so reverting core must refuse rather
-- than take them with it.

ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLES FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE workspace_email_domain FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE workspace FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE workflow_run FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE webhook_subscription FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE webhook_delivery FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE voice_profile_version FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE voice_profile_delta FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE voice_profile FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE voice_learning_signal FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE voice_corpus_source FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE voice_build FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE vault_secret FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE user_record_view FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE transcript_read FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE team_membership FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE team FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE taggable FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE tag FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE system_log FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE suggestion_dismissal FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE stage FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE site_read FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE signing_key FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE signal_thread_scan FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE signal_resolution FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE signal FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE setup_token FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE setting FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE session FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE scheduled_send FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE saved_view FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE runner_job FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE role_assignment FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE role FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE retention_policy FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE relationship FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE record_grant FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE raw_capture FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE quota FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE provider_run_reservation FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE provider_run FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE provider_connection_budget FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE provider_connection FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE project_phase_history FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE project FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE product FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE preference_token FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE pipeline FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE person_social FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE person_signature_enrich_state FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE person_provider_claim FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE person_profile_field FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE person_phone FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE person_moment_dismissal FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE person_email FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE person_consent FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE person_channel_identity FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE person_brief FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE person FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE passport FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE partner FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE organization_relationship_type FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE organization_profile_field FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE organization_open_pipeline_rollup FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE organization_geocode_state FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE organization_fact FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE organization_domain_disposition FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE organization_domain FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE organization FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE org_growth_fit FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE org_dossier FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE org_brief FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE onboarding_wizard_state FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE offer_template FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE offer_line_item FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE offer FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE oauth_refresh_token FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE oauth_grant FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE oauth_client FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE oauth_authorization_code FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE list_member FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE list FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE linkedin_connection FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE linkedin_account FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE lead_source FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE lead_score_history FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE lead_manual_signal FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE lead_disqualify_reason FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE lead FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE idempotency_key FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE graph_interaction_edge FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE geocode_cache FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE fx_rate FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE finance_payment FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE finance_invoice FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE finance_external_customer FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE finance_customer_link FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE finance_connection FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE field_provenance FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE field_mask FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE extension_secret FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE event_outbox FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE erasure_suppression FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE embedding FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE embed_store_binding FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE email_signature FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE dedupe_candidate FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE deal_stage_history FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE deal_forecast_history FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE deal FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE data_subject_request FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE custom_field FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE conversation_claim FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE contract FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE consent_qualifying_event FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE consent_purpose FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE consent_existing_customer_flag FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE consent_event FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE consent_doi_token FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE comms_outbound FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE commission_entry FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE channel_provider FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE channel_connection FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE capture_trace FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE capture_sync_state FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE capture_pending_counterparty FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE capture_freemail_domain FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE capture_exclusion FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE capture_digest FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE capture_connection FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE capture_backfill FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE capture_auto_enrich_state FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE capture_auto_enrich_budget FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE brief_run FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE brief_item FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE booking_page FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE automation FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE auth_token FROM margince_app;

REVOKE SELECT,INSERT ON TABLE audit_log FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE attachment_extraction FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE attachment FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE approval FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE app_user FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE ai_usage FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE ai_model_rate FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE ai_feedback FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE ai_call_payload FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE ai_call_config FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE ai_call FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE agent_task FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE agent_run FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE activity_retention_evidence FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE activity_participant_replay FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE activity_participant FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE activity_link FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE activity_kind FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE activity_audience_member FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE activity FROM margince_app;

REVOKE USAGE ON SCHEMA public FROM margince_app;

REVOKE USAGE ON SCHEMA ext FROM margince_app;

ALTER TABLE IF EXISTS webhook_subscription DROP CONSTRAINT IF EXISTS webhook_subscription_owner_fkey;

ALTER TABLE IF EXISTS webhook_delivery DROP CONSTRAINT IF EXISTS webhook_delivery_subscription_fkey;

ALTER TABLE IF EXISTS voice_profile_version DROP CONSTRAINT IF EXISTS voice_profile_version_profile_fkey;

ALTER TABLE IF EXISTS voice_profile_version DROP CONSTRAINT IF EXISTS voice_profile_version_predecessor_fkey;

ALTER TABLE IF EXISTS voice_profile DROP CONSTRAINT IF EXISTS voice_profile_team_fkey;

ALTER TABLE IF EXISTS voice_profile DROP CONSTRAINT IF EXISTS voice_profile_owner_fkey;

ALTER TABLE IF EXISTS voice_profile_delta DROP CONSTRAINT IF EXISTS voice_profile_delta_to_fkey;

ALTER TABLE IF EXISTS voice_profile_delta DROP CONSTRAINT IF EXISTS voice_profile_delta_profile_fkey;

ALTER TABLE IF EXISTS voice_profile_delta DROP CONSTRAINT IF EXISTS voice_profile_delta_from_fkey;

ALTER TABLE IF EXISTS voice_learning_signal DROP CONSTRAINT IF EXISTS voice_learning_signal_version_fkey;

ALTER TABLE IF EXISTS voice_learning_signal DROP CONSTRAINT IF EXISTS voice_learning_signal_profile_fkey;

ALTER TABLE IF EXISTS voice_corpus_source DROP CONSTRAINT IF EXISTS voice_corpus_source_profile_fkey;

ALTER TABLE IF EXISTS voice_build DROP CONSTRAINT IF EXISTS voice_build_result_version_fkey;

ALTER TABLE IF EXISTS voice_build DROP CONSTRAINT IF EXISTS voice_build_requester_fkey;

ALTER TABLE IF EXISTS voice_build DROP CONSTRAINT IF EXISTS voice_build_profile_fkey;

ALTER TABLE IF EXISTS user_record_view DROP CONSTRAINT IF EXISTS user_record_view_user_id_fkey;

ALTER TABLE IF EXISTS transcript_read DROP CONSTRAINT IF EXISTS transcript_read_activity_id_fkey;

ALTER TABLE IF EXISTS team DROP CONSTRAINT IF EXISTS team_parent_team_id_fkey;

ALTER TABLE IF EXISTS team_membership DROP CONSTRAINT IF EXISTS team_membership_user_id_fkey;

ALTER TABLE IF EXISTS team_membership DROP CONSTRAINT IF EXISTS team_membership_team_id_fkey;

ALTER TABLE IF EXISTS taggable DROP CONSTRAINT IF EXISTS taggable_tag_id_fkey;

ALTER TABLE IF EXISTS system_log DROP CONSTRAINT IF EXISTS system_log_workspace_id_fkey;

ALTER TABLE IF EXISTS system_log DROP CONSTRAINT IF EXISTS system_log_on_behalf_of_fkey;

ALTER TABLE IF EXISTS suggestion_dismissal DROP CONSTRAINT IF EXISTS suggestion_dismissal_user_id_fkey;

ALTER TABLE IF EXISTS suggestion_dismissal DROP CONSTRAINT IF EXISTS suggestion_dismissal_org_fkey;

ALTER TABLE IF EXISTS stage DROP CONSTRAINT IF EXISTS stage_pipeline_id_fkey;

ALTER TABLE IF EXISTS site_read DROP CONSTRAINT IF EXISTS site_read_org_fkey;

ALTER TABLE IF EXISTS signal_resolution DROP CONSTRAINT IF EXISTS sigres_signal_fkey;

ALTER TABLE IF EXISTS signal_resolution DROP CONSTRAINT IF EXISTS sigres_resolved_by_fkey;

ALTER TABLE IF EXISTS signal_resolution DROP CONSTRAINT IF EXISTS sigres_org_fkey;

ALTER TABLE IF EXISTS signal_thread_scan DROP CONSTRAINT IF EXISTS signal_thread_scan_resolved_org_fkey;

ALTER TABLE IF EXISTS signal DROP CONSTRAINT IF EXISTS signal_resolved_person_fkey;

ALTER TABLE IF EXISTS signal DROP CONSTRAINT IF EXISTS signal_resolved_org_fkey;

ALTER TABLE IF EXISTS signal DROP CONSTRAINT IF EXISTS signal_owner_fkey;

ALTER TABLE IF EXISTS session DROP CONSTRAINT IF EXISTS session_user_id_fkey;

ALTER TABLE IF EXISTS scheduled_send DROP CONSTRAINT IF EXISTS scheduled_send_scheduled_by_fkey;

ALTER TABLE IF EXISTS scheduled_send DROP CONSTRAINT IF EXISTS scheduled_send_delivery_id_fkey;

ALTER TABLE IF EXISTS scheduled_send DROP CONSTRAINT IF EXISTS scheduled_send_anchor_activity_id_fkey;

ALTER TABLE IF EXISTS scheduled_send DROP CONSTRAINT IF EXISTS scheduled_send_agent_on_behalf_of_fkey;

ALTER TABLE IF EXISTS scheduled_send DROP CONSTRAINT IF EXISTS scheduled_send_activity_id_fkey;

ALTER TABLE IF EXISTS saved_view DROP CONSTRAINT IF EXISTS saved_view_owner_fkey;

ALTER TABLE IF EXISTS runner_job DROP CONSTRAINT IF EXISTS runner_job_run_fkey;

ALTER TABLE IF EXISTS runner_job DROP CONSTRAINT IF EXISTS runner_job_passport_fkey;

ALTER TABLE IF EXISTS role_assignment DROP CONSTRAINT IF EXISTS role_assignment_user_id_fkey;

ALTER TABLE IF EXISTS role_assignment DROP CONSTRAINT IF EXISTS role_assignment_team_id_fkey;

ALTER TABLE IF EXISTS role_assignment DROP CONSTRAINT IF EXISTS role_assignment_role_id_fkey;

ALTER TABLE IF EXISTS relationship DROP CONSTRAINT IF EXISTS relationship_project_id_fkey;

ALTER TABLE IF EXISTS relationship DROP CONSTRAINT IF EXISTS relationship_person_id_fkey;

ALTER TABLE IF EXISTS relationship DROP CONSTRAINT IF EXISTS relationship_organization_id_fkey;

ALTER TABLE IF EXISTS relationship DROP CONSTRAINT IF EXISTS relationship_deal_id_fkey;

ALTER TABLE IF EXISTS relationship DROP CONSTRAINT IF EXISTS relationship_counterparty_org_id_fkey;

ALTER TABLE IF EXISTS record_grant DROP CONSTRAINT IF EXISTS record_grant_granted_by_fkey;

ALTER TABLE IF EXISTS quota DROP CONSTRAINT IF EXISTS quota_team_id_fkey;

ALTER TABLE IF EXISTS quota DROP CONSTRAINT IF EXISTS quota_owner_id_fkey;

ALTER TABLE IF EXISTS provider_run_reservation DROP CONSTRAINT IF EXISTS provider_run_reservation_run_id_fkey;

ALTER TABLE IF EXISTS provider_run DROP CONSTRAINT IF EXISTS provider_run_requested_by_fkey;

ALTER TABLE IF EXISTS provider_run DROP CONSTRAINT IF EXISTS provider_run_person_id_fkey;

ALTER TABLE IF EXISTS provider_connection DROP CONSTRAINT IF EXISTS provider_connection_connected_by_fkey;

ALTER TABLE IF EXISTS provider_connection_budget DROP CONSTRAINT IF EXISTS provider_connection_budget_connection_id_fkey;

ALTER TABLE IF EXISTS project_phase_history DROP CONSTRAINT IF EXISTS project_phase_history_project_id_fkey;

ALTER TABLE IF EXISTS project DROP CONSTRAINT IF EXISTS project_owner_id_fkey;

ALTER TABLE IF EXISTS project DROP CONSTRAINT IF EXISTS project_organization_id_fkey;

ALTER TABLE IF EXISTS preference_token DROP CONSTRAINT IF EXISTS preference_token_person_fkey;

ALTER TABLE IF EXISTS person_social DROP CONSTRAINT IF EXISTS person_social_person_id_fkey;

ALTER TABLE IF EXISTS person_signature_enrich_state DROP CONSTRAINT IF EXISTS person_signature_enrich_state_person_id_fkey;

ALTER TABLE IF EXISTS person_signature_enrich_state DROP CONSTRAINT IF EXISTS person_signature_enrich_state_activity_id_fkey;

ALTER TABLE IF EXISTS person_provider_claim DROP CONSTRAINT IF EXISTS person_provider_claim_run_id_fkey;

ALTER TABLE IF EXISTS person_provider_claim DROP CONSTRAINT IF EXISTS person_provider_claim_person_id_fkey;

ALTER TABLE IF EXISTS person_profile_field DROP CONSTRAINT IF EXISTS person_profile_field_person_fk;

ALTER TABLE IF EXISTS person_phone DROP CONSTRAINT IF EXISTS person_phone_person_id_fkey;

ALTER TABLE IF EXISTS person DROP CONSTRAINT IF EXISTS person_owner_id_fkey;

ALTER TABLE IF EXISTS person_moment_dismissal DROP CONSTRAINT IF EXISTS person_moment_dismissal_user_fkey;

ALTER TABLE IF EXISTS person_moment_dismissal DROP CONSTRAINT IF EXISTS person_moment_dismissal_person_fkey;

ALTER TABLE IF EXISTS person DROP CONSTRAINT IF EXISTS person_merged_into_id_fkey;

ALTER TABLE IF EXISTS person_email DROP CONSTRAINT IF EXISTS person_email_person_id_fkey;

ALTER TABLE IF EXISTS person DROP CONSTRAINT IF EXISTS person_converted_from_lead_id_fkey;

ALTER TABLE IF EXISTS person_consent DROP CONSTRAINT IF EXISTS person_consent_purpose_id_fkey;

ALTER TABLE IF EXISTS person_consent DROP CONSTRAINT IF EXISTS person_consent_person_id_fkey;

ALTER TABLE IF EXISTS person_consent DROP CONSTRAINT IF EXISTS person_consent_lead_id_fkey;

ALTER TABLE IF EXISTS person_channel_identity DROP CONSTRAINT IF EXISTS person_channel_identity_provider_fkey;

ALTER TABLE IF EXISTS person_channel_identity DROP CONSTRAINT IF EXISTS person_channel_identity_person_id_fkey;

ALTER TABLE IF EXISTS person_brief DROP CONSTRAINT IF EXISTS person_brief_user_fkey;

ALTER TABLE IF EXISTS person_brief DROP CONSTRAINT IF EXISTS person_brief_person_fkey;

ALTER TABLE IF EXISTS passport DROP CONSTRAINT IF EXISTS passport_on_behalf_of_fkey;

ALTER TABLE IF EXISTS passport DROP CONSTRAINT IF EXISTS passport_granted_by_fkey;

ALTER TABLE IF EXISTS passport DROP CONSTRAINT IF EXISTS passport_grant_fkey;

ALTER TABLE IF EXISTS partner DROP CONSTRAINT IF EXISTS partner_organization_id_fkey;

ALTER TABLE IF EXISTS organization_relationship_type DROP CONSTRAINT IF EXISTS organization_relationship_typ_workspace_id_organization_id_fkey;

ALTER TABLE IF EXISTS organization DROP CONSTRAINT IF EXISTS organization_parent_org_id_fkey;

ALTER TABLE IF EXISTS organization DROP CONSTRAINT IF EXISTS organization_owner_id_fkey;

ALTER TABLE IF EXISTS organization DROP CONSTRAINT IF EXISTS organization_merged_into_id_fkey;

ALTER TABLE IF EXISTS organization_geocode_state DROP CONSTRAINT IF EXISTS organization_geocode_state_organization_id_fkey;

ALTER TABLE IF EXISTS organization_domain DROP CONSTRAINT IF EXISTS organization_domain_organization_id_fkey;

ALTER TABLE IF EXISTS organization_domain_disposition DROP CONSTRAINT IF EXISTS organization_domain_disposition_site_read_fkey;

ALTER TABLE IF EXISTS organization_domain_disposition DROP CONSTRAINT IF EXISTS organization_domain_disposition_owner_fkey;

ALTER TABLE IF EXISTS organization_domain_disposition DROP CONSTRAINT IF EXISTS organization_domain_disposition_org_fkey;

ALTER TABLE IF EXISTS organization_profile_field DROP CONSTRAINT IF EXISTS org_profile_field_org_fkey;

ALTER TABLE IF EXISTS org_growth_fit DROP CONSTRAINT IF EXISTS org_growth_fit_user_fkey;

ALTER TABLE IF EXISTS org_growth_fit DROP CONSTRAINT IF EXISTS org_growth_fit_org_fkey;

ALTER TABLE IF EXISTS organization_fact DROP CONSTRAINT IF EXISTS org_fact_site_read_fkey;

ALTER TABLE IF EXISTS organization_fact DROP CONSTRAINT IF EXISTS org_fact_org_fkey;

ALTER TABLE IF EXISTS org_dossier DROP CONSTRAINT IF EXISTS org_dossier_user_fkey;

ALTER TABLE IF EXISTS org_dossier DROP CONSTRAINT IF EXISTS org_dossier_org_fkey;

ALTER TABLE IF EXISTS org_brief DROP CONSTRAINT IF EXISTS org_brief_user_id_fkey;

ALTER TABLE IF EXISTS org_brief DROP CONSTRAINT IF EXISTS org_brief_org_fkey;

ALTER TABLE IF EXISTS onboarding_wizard_state DROP CONSTRAINT IF EXISTS onboarding_wizard_state_user_fkey;

ALTER TABLE IF EXISTS onboarding_wizard_state DROP CONSTRAINT IF EXISTS onboarding_wizard_state_read_fkey;

ALTER TABLE IF EXISTS offer_line_item DROP CONSTRAINT IF EXISTS oli_product_fkey;

ALTER TABLE IF EXISTS offer_line_item DROP CONSTRAINT IF EXISTS oli_offer_fkey;

ALTER TABLE IF EXISTS offer DROP CONSTRAINT IF EXISTS offer_template_id_fkey;

ALTER TABLE IF EXISTS offer DROP CONSTRAINT IF EXISTS offer_deal_fkey;

ALTER TABLE IF EXISTS offer DROP CONSTRAINT IF EXISTS offer_buyer_org_fkey;

ALTER TABLE IF EXISTS oauth_refresh_token DROP CONSTRAINT IF EXISTS oauth_refresh_grant_fkey;

ALTER TABLE IF EXISTS oauth_grant DROP CONSTRAINT IF EXISTS oauth_grant_user_fkey;

ALTER TABLE IF EXISTS oauth_grant DROP CONSTRAINT IF EXISTS oauth_grant_lent_passport_fkey;

ALTER TABLE IF EXISTS oauth_grant DROP CONSTRAINT IF EXISTS oauth_grant_client_fkey;

ALTER TABLE IF EXISTS oauth_authorization_code DROP CONSTRAINT IF EXISTS oauth_code_user_fkey;

ALTER TABLE IF EXISTS oauth_authorization_code DROP CONSTRAINT IF EXISTS oauth_code_lent_passport_fkey;

ALTER TABLE IF EXISTS list DROP CONSTRAINT IF EXISTS list_team_id_fkey;

ALTER TABLE IF EXISTS list DROP CONSTRAINT IF EXISTS list_owner_id_fkey;

ALTER TABLE IF EXISTS list_member DROP CONSTRAINT IF EXISTS list_member_list_id_fkey;

ALTER TABLE IF EXISTS linkedin_connection DROP CONSTRAINT IF EXISTS linkedin_connection_workspace_id_owner_user_id_fkey;

ALTER TABLE IF EXISTS linkedin_connection DROP CONSTRAINT IF EXISTS linkedin_connection_workspace_id_matched_person_id_fkey;

ALTER TABLE IF EXISTS linkedin_connection DROP CONSTRAINT IF EXISTS linkedin_connection_workspace_id_matched_org_id_fkey;

ALTER TABLE IF EXISTS linkedin_account DROP CONSTRAINT IF EXISTS linkedin_account_workspace_id_user_id_fkey;

ALTER TABLE IF EXISTS lead_score_history DROP CONSTRAINT IF EXISTS lead_score_history_lead_id_fkey;

ALTER TABLE IF EXISTS lead DROP CONSTRAINT IF EXISTS lead_qualified_deal_id_fkey;

ALTER TABLE IF EXISTS lead DROP CONSTRAINT IF EXISTS lead_promoted_person_id_fkey;

ALTER TABLE IF EXISTS lead DROP CONSTRAINT IF EXISTS lead_project_id_fkey;

ALTER TABLE IF EXISTS lead DROP CONSTRAINT IF EXISTS lead_owner_id_fkey;

ALTER TABLE IF EXISTS lead DROP CONSTRAINT IF EXISTS lead_merged_into_id_fkey;

ALTER TABLE IF EXISTS lead_manual_signal DROP CONSTRAINT IF EXISTS lead_manual_signal_set_by_fkey;

ALTER TABLE IF EXISTS lead_manual_signal DROP CONSTRAINT IF EXISTS lead_manual_signal_lead_id_fkey;

ALTER TABLE IF EXISTS lead DROP CONSTRAINT IF EXISTS lead_disqualify_reason_id_fkey;

ALTER TABLE IF EXISTS graph_interaction_edge DROP CONSTRAINT IF EXISTS graph_interaction_edge_workspace_id_user_id_fkey;

ALTER TABLE IF EXISTS graph_interaction_edge DROP CONSTRAINT IF EXISTS graph_interaction_edge_workspace_id_person_id_fkey;

ALTER TABLE IF EXISTS finance_payment DROP CONSTRAINT IF EXISTS finance_payment_organization_fk;

ALTER TABLE IF EXISTS finance_payment DROP CONSTRAINT IF EXISTS finance_payment_invoice_fk;

ALTER TABLE IF EXISTS finance_payment DROP CONSTRAINT IF EXISTS finance_payment_connection_fk;

ALTER TABLE IF EXISTS finance_invoice DROP CONSTRAINT IF EXISTS finance_invoice_organization_fk;

ALTER TABLE IF EXISTS finance_invoice DROP CONSTRAINT IF EXISTS finance_invoice_credits_fk;

ALTER TABLE IF EXISTS finance_invoice DROP CONSTRAINT IF EXISTS finance_invoice_connection_fk;

ALTER TABLE IF EXISTS finance_external_customer DROP CONSTRAINT IF EXISTS finance_external_customer_connection_fk;

ALTER TABLE IF EXISTS finance_customer_link DROP CONSTRAINT IF EXISTS finance_customer_link_organization_fk;

ALTER TABLE IF EXISTS finance_customer_link DROP CONSTRAINT IF EXISTS finance_customer_link_connection_fk;

ALTER TABLE IF EXISTS extension_secret DROP CONSTRAINT IF EXISTS extension_secret_workspace_id_user_id_fkey;

ALTER TABLE IF EXISTS email_signature DROP CONSTRAINT IF EXISTS email_signature_owner_id_fkey;

ALTER TABLE IF EXISTS data_subject_request DROP CONSTRAINT IF EXISTS dsr_assignee_fkey;

ALTER TABLE IF EXISTS dedupe_candidate DROP CONSTRAINT IF EXISTS dedupe_candidate_right_person_id_fkey;

ALTER TABLE IF EXISTS dedupe_candidate DROP CONSTRAINT IF EXISTS dedupe_candidate_right_org_id_fkey;

ALTER TABLE IF EXISTS dedupe_candidate DROP CONSTRAINT IF EXISTS dedupe_candidate_right_lead_id_fkey;

ALTER TABLE IF EXISTS dedupe_candidate DROP CONSTRAINT IF EXISTS dedupe_candidate_left_person_id_fkey;

ALTER TABLE IF EXISTS dedupe_candidate DROP CONSTRAINT IF EXISTS dedupe_candidate_left_org_id_fkey;

ALTER TABLE IF EXISTS dedupe_candidate DROP CONSTRAINT IF EXISTS dedupe_candidate_left_lead_id_fkey;

ALTER TABLE IF EXISTS dedupe_candidate DROP CONSTRAINT IF EXISTS dedupe_candidate_disposed_by_fkey;

ALTER TABLE IF EXISTS deal DROP CONSTRAINT IF EXISTS deal_stage_in_pipeline;

ALTER TABLE IF EXISTS deal DROP CONSTRAINT IF EXISTS deal_stage_id_fkey;

ALTER TABLE IF EXISTS deal_stage_history DROP CONSTRAINT IF EXISTS deal_stage_history_to_stage_id_fkey;

ALTER TABLE IF EXISTS deal_stage_history DROP CONSTRAINT IF EXISTS deal_stage_history_from_stage_id_fkey;

ALTER TABLE IF EXISTS deal_stage_history DROP CONSTRAINT IF EXISTS deal_stage_history_deal_id_fkey;

ALTER TABLE IF EXISTS deal DROP CONSTRAINT IF EXISTS deal_project_id_fkey;

ALTER TABLE IF EXISTS deal DROP CONSTRAINT IF EXISTS deal_pipeline_id_fkey;

ALTER TABLE IF EXISTS deal DROP CONSTRAINT IF EXISTS deal_partner_org_id_fkey;

ALTER TABLE IF EXISTS deal DROP CONSTRAINT IF EXISTS deal_owner_id_fkey;

ALTER TABLE IF EXISTS deal DROP CONSTRAINT IF EXISTS deal_organization_id_fkey;

ALTER TABLE IF EXISTS deal_forecast_history DROP CONSTRAINT IF EXISTS deal_forecast_history_deal_id_fkey;

ALTER TABLE IF EXISTS custom_field DROP CONSTRAINT IF EXISTS custom_field_created_by_fkey;

ALTER TABLE IF EXISTS conversation_claim DROP CONSTRAINT IF EXISTS conversation_claim_task_fkey;

ALTER TABLE IF EXISTS conversation_claim DROP CONSTRAINT IF EXISTS conversation_claim_person_fkey;

ALTER TABLE IF EXISTS conversation_claim DROP CONSTRAINT IF EXISTS conversation_claim_corrector_fkey;

ALTER TABLE IF EXISTS conversation_claim DROP CONSTRAINT IF EXISTS conversation_claim_activity_fkey;

ALTER TABLE IF EXISTS contract DROP CONSTRAINT IF EXISTS contract_superseded_by_id_fkey;

ALTER TABLE IF EXISTS contract DROP CONSTRAINT IF EXISTS contract_project_id_fkey;

ALTER TABLE IF EXISTS contract DROP CONSTRAINT IF EXISTS contract_organization_id_fkey;

ALTER TABLE IF EXISTS contract DROP CONSTRAINT IF EXISTS contract_deal_id_fkey;

ALTER TABLE IF EXISTS consent_qualifying_event DROP CONSTRAINT IF EXISTS consent_qualifying_event_person_fkey;

ALTER TABLE IF EXISTS consent_existing_customer_flag DROP CONSTRAINT IF EXISTS consent_existing_customer_setter_fkey;

ALTER TABLE IF EXISTS consent_existing_customer_flag DROP CONSTRAINT IF EXISTS consent_existing_customer_person_fkey;

ALTER TABLE IF EXISTS consent_event DROP CONSTRAINT IF EXISTS consent_event_purpose_id_fkey;

ALTER TABLE IF EXISTS consent_event DROP CONSTRAINT IF EXISTS consent_event_person_id_fkey;

ALTER TABLE IF EXISTS consent_event DROP CONSTRAINT IF EXISTS consent_event_lead_id_fkey;

ALTER TABLE IF EXISTS consent_doi_token DROP CONSTRAINT IF EXISTS consent_doi_token_purpose_id_fkey;

ALTER TABLE IF EXISTS consent_doi_token DROP CONSTRAINT IF EXISTS consent_doi_token_person_id_fkey;

ALTER TABLE IF EXISTS comms_outbound DROP CONSTRAINT IF EXISTS comms_outbound_user_id_fkey;

ALTER TABLE IF EXISTS comms_outbound DROP CONSTRAINT IF EXISTS comms_outbound_activity_id_fkey;

ALTER TABLE IF EXISTS commission_entry DROP CONSTRAINT IF EXISTS commission_entry_reversal_of_fkey;

ALTER TABLE IF EXISTS commission_entry DROP CONSTRAINT IF EXISTS commission_entry_partner_org_id_fkey;

ALTER TABLE IF EXISTS commission_entry DROP CONSTRAINT IF EXISTS commission_entry_deal_id_fkey;

ALTER TABLE IF EXISTS channel_connection DROP CONSTRAINT IF EXISTS channel_connection_connected_by_fkey;

ALTER TABLE IF EXISTS capture_sync_state DROP CONSTRAINT IF EXISTS capture_sync_state_connection_id_fkey;

ALTER TABLE IF EXISTS capture_pending_counterparty DROP CONSTRAINT IF EXISTS capture_pending_counterparty_proposal_id_fkey;

ALTER TABLE IF EXISTS capture_pending_counterparty DROP CONSTRAINT IF EXISTS capture_pending_counterparty_owner_id_fkey;

ALTER TABLE IF EXISTS capture_pending_counterparty DROP CONSTRAINT IF EXISTS capture_pending_counterparty_activity_id_fkey;

ALTER TABLE IF EXISTS capture_freemail_domain DROP CONSTRAINT IF EXISTS capture_freemail_domain_created_by_fkey;

ALTER TABLE IF EXISTS capture_exclusion DROP CONSTRAINT IF EXISTS capture_exclusion_user_id_fkey;

ALTER TABLE IF EXISTS capture_digest DROP CONSTRAINT IF EXISTS capture_digest_user_id_fkey;

ALTER TABLE IF EXISTS capture_connection DROP CONSTRAINT IF EXISTS capture_connection_user_id_fkey;

ALTER TABLE IF EXISTS capture_backfill DROP CONSTRAINT IF EXISTS capture_backfill_connection_fkey;

ALTER TABLE IF EXISTS capture_auto_enrich_state DROP CONSTRAINT IF EXISTS capture_auto_enrich_state_organization_id_fkey;

ALTER TABLE IF EXISTS brief_run DROP CONSTRAINT IF EXISTS brief_run_user_fkey;

ALTER TABLE IF EXISTS brief_item DROP CONSTRAINT IF EXISTS brief_item_run_fkey;

ALTER TABLE IF EXISTS brief_item DROP CONSTRAINT IF EXISTS brief_item_deal_fkey;

ALTER TABLE IF EXISTS booking_page DROP CONSTRAINT IF EXISTS booking_page_host_fkey;

ALTER TABLE IF EXISTS automation DROP CONSTRAINT IF EXISTS automation_owner_fkey;

ALTER TABLE IF EXISTS auth_token DROP CONSTRAINT IF EXISTS auth_token_user_fkey;

ALTER TABLE IF EXISTS audit_log DROP CONSTRAINT IF EXISTS audit_log_workspace_id_fkey;

ALTER TABLE IF EXISTS audit_log DROP CONSTRAINT IF EXISTS audit_log_on_behalf_of_fkey;

ALTER TABLE IF EXISTS attachment DROP CONSTRAINT IF EXISTS attachment_supersedes_fkey;

ALTER TABLE IF EXISTS attachment_extraction DROP CONSTRAINT IF EXISTS attachment_extraction_attachment_id_fkey;

ALTER TABLE IF EXISTS attachment DROP CONSTRAINT IF EXISTS attachment_contract_id_fkey;

ALTER TABLE IF EXISTS approval DROP CONSTRAINT IF EXISTS approval_passport_id_fkey;

ALTER TABLE IF EXISTS approval DROP CONSTRAINT IF EXISTS approval_on_behalf_of_fkey;

ALTER TABLE IF EXISTS approval DROP CONSTRAINT IF EXISTS approval_decided_by_fkey;

ALTER TABLE IF EXISTS ai_call_payload DROP CONSTRAINT IF EXISTS ai_call_payload_ai_call_fkey;

ALTER TABLE IF EXISTS ai_call DROP CONSTRAINT IF EXISTS ai_call_config_fk;

ALTER TABLE IF EXISTS agent_task DROP CONSTRAINT IF EXISTS agent_task_passport_fk;

ALTER TABLE IF EXISTS agent_task DROP CONSTRAINT IF EXISTS agent_task_approval_fk;

ALTER TABLE IF EXISTS agent_run DROP CONSTRAINT IF EXISTS agent_run_passport_fkey;

ALTER TABLE IF EXISTS agent_run DROP CONSTRAINT IF EXISTS agent_run_approval_fkey;

ALTER TABLE IF EXISTS activity_retention_evidence DROP CONSTRAINT IF EXISTS activity_retention_evidence_decided_by_fkey;

ALTER TABLE IF EXISTS activity_retention_evidence DROP CONSTRAINT IF EXISTS activity_retention_evidence_deal_id_fkey;

ALTER TABLE IF EXISTS activity_retention_evidence DROP CONSTRAINT IF EXISTS activity_retention_evidence_activity_id_fkey;

ALTER TABLE IF EXISTS activity_participant DROP CONSTRAINT IF EXISTS activity_participant_user_fkey;

ALTER TABLE IF EXISTS activity_participant_replay DROP CONSTRAINT IF EXISTS activity_participant_replay_activity_fkey;

ALTER TABLE IF EXISTS activity_participant DROP CONSTRAINT IF EXISTS activity_participant_person_fkey;

ALTER TABLE IF EXISTS activity_participant DROP CONSTRAINT IF EXISTS activity_participant_activity_fkey;

ALTER TABLE IF EXISTS activity_link DROP CONSTRAINT IF EXISTS activity_link_project_id_fkey;

ALTER TABLE IF EXISTS activity_link DROP CONSTRAINT IF EXISTS activity_link_person_id_fkey;

ALTER TABLE IF EXISTS activity_link DROP CONSTRAINT IF EXISTS activity_link_organization_id_fkey;

ALTER TABLE IF EXISTS activity_link DROP CONSTRAINT IF EXISTS activity_link_lead_id_fkey;

ALTER TABLE IF EXISTS activity_link DROP CONSTRAINT IF EXISTS activity_link_deal_id_fkey;

ALTER TABLE IF EXISTS activity_link DROP CONSTRAINT IF EXISTS activity_link_activity_id_fkey;

ALTER TABLE IF EXISTS activity DROP CONSTRAINT IF EXISTS activity_kind_fkey;

ALTER TABLE IF EXISTS activity DROP CONSTRAINT IF EXISTS activity_host_user_id_fkey;

ALTER TABLE IF EXISTS activity DROP CONSTRAINT IF EXISTS activity_channel_provider_fkey;

ALTER TABLE IF EXISTS activity_audience_member DROP CONSTRAINT IF EXISTS activity_audience_member_activity_id_fkey;

ALTER TABLE IF EXISTS activity DROP CONSTRAINT IF EXISTS activity_assignee_id_fkey;

DROP TRIGGER IF EXISTS trg_workspace_updated ON workspace;

DROP TRIGGER IF EXISTS trg_webhook_subscription_updated ON webhook_subscription;

DROP TRIGGER IF EXISTS trg_webhook_delivery_updated ON webhook_delivery;

DROP TRIGGER IF EXISTS trg_team_updated ON team;

DROP TRIGGER IF EXISTS trg_team_membership_updated ON team_membership;

DROP TRIGGER IF EXISTS trg_tag_updated ON tag;

DROP TRIGGER IF EXISTS trg_system_log_no_mutate ON system_log;

DROP TRIGGER IF EXISTS trg_stage_updated ON stage;

DROP TRIGGER IF EXISTS trg_signal_updated ON signal;

DROP TRIGGER IF EXISTS trg_saved_view_updated ON saved_view;

DROP TRIGGER IF EXISTS trg_role_updated ON role;

DROP TRIGGER IF EXISTS trg_role_assignment_updated ON role_assignment;

DROP TRIGGER IF EXISTS trg_relationship_updated ON relationship;

DROP TRIGGER IF EXISTS trg_quota_updated ON quota;

DROP TRIGGER IF EXISTS trg_provider_run_updated ON provider_run;

DROP TRIGGER IF EXISTS trg_provider_connection_updated ON provider_connection;

DROP TRIGGER IF EXISTS trg_project_updated ON project;

DROP TRIGGER IF EXISTS trg_product_updated ON product;

DROP TRIGGER IF EXISTS trg_pipeline_updated ON pipeline;

DROP TRIGGER IF EXISTS trg_person_updated ON person;

DROP TRIGGER IF EXISTS trg_person_profile_field_updated ON person_profile_field;

DROP TRIGGER IF EXISTS trg_person_phone_updated ON person_phone;

DROP TRIGGER IF EXISTS trg_person_email_updated ON person_email;

DROP TRIGGER IF EXISTS trg_person_channel_identity_updated ON person_channel_identity;

DROP TRIGGER IF EXISTS trg_partner_updated ON partner;

DROP TRIGGER IF EXISTS trg_organization_updated ON organization;

DROP TRIGGER IF EXISTS trg_organization_relationship_type_updated ON organization_relationship_type;

DROP TRIGGER IF EXISTS trg_organization_profile_field_updated ON organization_profile_field;

DROP TRIGGER IF EXISTS trg_organization_no_cycle ON organization;

DROP TRIGGER IF EXISTS trg_organization_geocode_stale ON organization;

DROP TRIGGER IF EXISTS trg_organization_fact_updated ON organization_fact;

DROP TRIGGER IF EXISTS trg_organization_domain_updated ON organization_domain;

DROP TRIGGER IF EXISTS trg_oli_updated ON offer_line_item;

DROP TRIGGER IF EXISTS trg_offer_updated ON offer;

DROP TRIGGER IF EXISTS trg_offer_template_updated ON offer_template;

DROP TRIGGER IF EXISTS trg_list_updated ON list;

DROP TRIGGER IF EXISTS trg_lead_updated ON lead;

DROP TRIGGER IF EXISTS trg_lead_source_updated ON lead_source;

DROP TRIGGER IF EXISTS trg_lead_disqualify_reason_updated ON lead_disqualify_reason;

DROP TRIGGER IF EXISTS trg_finance_payment_updated ON finance_payment;

DROP TRIGGER IF EXISTS trg_finance_invoice_updated ON finance_invoice;

DROP TRIGGER IF EXISTS trg_finance_external_customer_updated ON finance_external_customer;

DROP TRIGGER IF EXISTS trg_finance_customer_link_updated ON finance_customer_link;

DROP TRIGGER IF EXISTS trg_finance_connection_updated ON finance_connection;

DROP TRIGGER IF EXISTS trg_email_signature_updated ON email_signature;

DROP TRIGGER IF EXISTS trg_dsr_updated ON data_subject_request;

DROP TRIGGER IF EXISTS trg_dedupe_candidate_updated ON dedupe_candidate;

DROP TRIGGER IF EXISTS trg_deal_updated ON deal;

DROP TRIGGER IF EXISTS trg_deal_project_same_org ON deal;

DROP TRIGGER IF EXISTS trg_custom_field_updated ON custom_field;

DROP TRIGGER IF EXISTS trg_conversation_claim_updated ON conversation_claim;

DROP TRIGGER IF EXISTS trg_channel_connection_updated ON channel_connection;

DROP TRIGGER IF EXISTS trg_capture_sync_state_updated ON capture_sync_state;

DROP TRIGGER IF EXISTS trg_capture_backfill_updated ON capture_backfill;

DROP TRIGGER IF EXISTS trg_audit_no_mutate ON audit_log;

DROP TRIGGER IF EXISTS trg_attachment_updated ON attachment;

DROP TRIGGER IF EXISTS trg_approval_updated ON approval;

DROP TRIGGER IF EXISTS trg_app_user_updated ON app_user;

DROP TRIGGER IF EXISTS trg_activity_updated ON activity;

DROP TRIGGER IF EXISTS relationship_last_activity ON relationship;

DROP TRIGGER IF EXISTS organization_refuse_anchor_retirement ON organization;

DROP TRIGGER IF EXISTS organization_delete_clears_deal_partner ON organization;

DROP TRIGGER IF EXISTS deal_last_activity ON deal;

DROP TRIGGER IF EXISTS contract_set_updated_at ON contract;

DROP TRIGGER IF EXISTS commission_entry_touch ON commission_entry;

DROP TRIGGER IF EXISTS activity_retention_evidence_is_frozen ON activity_retention_evidence;

DROP TRIGGER IF EXISTS activity_refuse_restricted_mutation ON activity;

DROP TRIGGER IF EXISTS activity_project_last_activity ON activity;

DROP TRIGGER IF EXISTS activity_link_project_last_activity ON activity_link;

DROP TRIGGER IF EXISTS activity_link_last_activity ON activity_link;

DROP TRIGGER IF EXISTS activity_last_activity ON activity;

DROP INDEX IF EXISTS voice_profile_version_profile_fk;

DROP INDEX IF EXISTS voice_profile_version_one_active;

DROP INDEX IF EXISTS voice_profile_version_history;

DROP INDEX IF EXISTS voice_profile_team_fk;

DROP INDEX IF EXISTS voice_profile_owner_fk;

DROP INDEX IF EXISTS voice_profile_delta_profile_fk;

DROP INDEX IF EXISTS voice_profile_delta_history;

DROP INDEX IF EXISTS voice_learning_signal_retention;

DROP INDEX IF EXISTS voice_learning_signal_profile_fk;

DROP INDEX IF EXISTS voice_corpus_source_profile_fk;

DROP INDEX IF EXISTS voice_corpus_source_manifest;

DROP INDEX IF EXISTS voice_build_requester_fk;

DROP INDEX IF EXISTS voice_build_profile_fk;

DROP INDEX IF EXISTS voice_build_poll;

DROP INDEX IF EXISTS voice_build_one_active;

DROP INDEX IF EXISTS voice_build_deferred_due;

DROP INDEX IF EXISTS uq_voice_profile_user_live;

DROP INDEX IF EXISTS uq_transcript_read_inflight;

DROP INDEX IF EXISTS uq_tag_name;

DROP INDEX IF EXISTS uq_stage_position;

DROP INDEX IF EXISTS uq_site_read_triage_inflight;

DROP INDEX IF EXISTS uq_site_read_org_inflight;

DROP INDEX IF EXISTS uq_site_read_onboarding_inflight;

DROP INDEX IF EXISTS uq_signal_fingerprint;

DROP INDEX IF EXISTS uq_role_assignment;

DROP INDEX IF EXISTS uq_rel_project_stakeholder;

DROP INDEX IF EXISTS uq_rel_employment;

DROP INDEX IF EXISTS uq_rel_deal_person_role;

DROP INDEX IF EXISTS uq_rel_current_primary_employer;

DROP INDEX IF EXISTS uq_project_key;

DROP INDEX IF EXISTS uq_product_sku;

DROP INDEX IF EXISTS uq_preference_token_person;

DROP INDEX IF EXISTS uq_pipeline_default;

DROP INDEX IF EXISTS uq_person_phone_primary;

DROP INDEX IF EXISTS uq_person_email_primary;

DROP INDEX IF EXISTS uq_person_email_dedupe;

DROP INDEX IF EXISTS uq_person_channel_identity;

DROP INDEX IF EXISTS uq_organization_domain_disposition;

DROP INDEX IF EXISTS uq_organization_anchor;

DROP INDEX IF EXISTS uq_org_rel_type;

DROP INDEX IF EXISTS uq_org_domain_primary;

DROP INDEX IF EXISTS uq_org_domain;

DROP INDEX IF EXISTS uq_offer_template_default;

DROP INDEX IF EXISTS uq_linkedin_connection_provider;

DROP INDEX IF EXISTS uq_linkedin_connection_natural;

DROP INDEX IF EXISTS uq_lead_source_key;

DROP INDEX IF EXISTS uq_lead_source;

DROP INDEX IF EXISTS uq_lead_manual_signal_live;

DROP INDEX IF EXISTS uq_lead_email_dedupe;

DROP INDEX IF EXISTS uq_dedupe_candidate_pair;

DROP INDEX IF EXISTS uq_custom_field_slug;

DROP INDEX IF EXISTS uq_custom_field_column;

DROP INDEX IF EXISTS uq_commission_trigger_event;

DROP INDEX IF EXISTS uq_commission_live_per_deal;

DROP INDEX IF EXISTS uq_channel_connection_ws;

DROP INDEX IF EXISTS uq_capture_freemail_domain;

DROP INDEX IF EXISTS uq_capture_exclusion;

DROP INDEX IF EXISTS uq_capture_backfill_live;

DROP INDEX IF EXISTS uq_attachment_extraction_inflight;

DROP INDEX IF EXISTS uq_app_user_email;

DROP INDEX IF EXISTS uq_activity_source;

DROP INDEX IF EXISTS uq_activity_retention_evidence;

DROP INDEX IF EXISTS uq_activity_participant;

DROP INDEX IF EXISTS uq_activity_link_project;

DROP INDEX IF EXISTS uq_activity_link;

DROP INDEX IF EXISTS suggestion_dismissal_organization_ix;

DROP INDEX IF EXISTS signal_resolved_org_ix;

DROP INDEX IF EXISTS signal_entity_ix;

DROP INDEX IF EXISTS setup_token_one_outstanding;

DROP INDEX IF EXISTS provider_run_person_history;

DROP INDEX IF EXISTS provider_run_one_live_person_fingerprint;

DROP INDEX IF EXISTS provider_run_due;

DROP INDEX IF EXISTS person_provider_claim_latest;

DROP INDEX IF EXISTS person_moment_dismissal_person_ix;

DROP INDEX IF EXISTS person_brief_person_ix;

DROP INDEX IF EXISTS passport_oauth_grant_ix;

DROP INDEX IF EXISTS organization_linkedin_url_key;

DROP INDEX IF EXISTS org_growth_fit_organization_ix;

DROP INDEX IF EXISTS org_dossier_organization_ix;

DROP INDEX IF EXISTS org_brief_organization_ix;

DROP INDEX IF EXISTS oauth_refresh_token_grant_ix;

DROP INDEX IF EXISTS oauth_grant_user_live_ix;

DROP INDEX IF EXISTS oauth_grant_lent_passport_ix;

DROP INDEX IF EXISTS oauth_code_lent_passport_ix;

DROP INDEX IF EXISTS idx_webhook_subscription_live;

DROP INDEX IF EXISTS idx_webhook_delivery_due;

DROP INDEX IF EXISTS idx_webhook_delivery_by_subscription;

DROP INDEX IF EXISTS idx_voice_corpus_profile;

DROP INDEX IF EXISTS idx_transcript_read_latest;

DROP INDEX IF EXISTS idx_team_membership_user;

DROP INDEX IF EXISTS idx_team_membership_team;

DROP INDEX IF EXISTS idx_taggable_tag;

DROP INDEX IF EXISTS idx_taggable_entity;

DROP INDEX IF EXISTS idx_system_log_time;

DROP INDEX IF EXISTS idx_system_log_actor;

DROP INDEX IF EXISTS idx_system_log_action;

DROP INDEX IF EXISTS idx_stage_pipeline;

DROP INDEX IF EXISTS idx_site_read_retry_due;

DROP INDEX IF EXISTS idx_site_read_org;

DROP INDEX IF EXISTS idx_sigres_signal;

DROP INDEX IF EXISTS idx_signal_unresolved;

DROP INDEX IF EXISTS idx_signal_owner_private;

DROP INDEX IF EXISTS idx_signal_open;

DROP INDEX IF EXISTS idx_session_user;

DROP INDEX IF EXISTS idx_scheduled_send_owner;

DROP INDEX IF EXISTS idx_scheduled_send_due;

DROP INDEX IF EXISTS idx_scheduled_send_anchor;

DROP INDEX IF EXISTS idx_saved_view_owner;

DROP INDEX IF EXISTS idx_runner_job_due;

DROP INDEX IF EXISTS idx_role_assignment_user;

DROP INDEX IF EXISTS idx_rel_traverse_project;

DROP INDEX IF EXISTS idx_rel_traverse_person;

DROP INDEX IF EXISTS idx_rel_traverse_organization;

DROP INDEX IF EXISTS idx_rel_traverse_deal;

DROP INDEX IF EXISTS idx_rel_stakeholder_deals;

DROP INDEX IF EXISTS idx_rel_project_stakeholders;

DROP INDEX IF EXISTS idx_rel_person_projects;

DROP INDEX IF EXISTS idx_rel_person_orgs;

DROP INDEX IF EXISTS idx_rel_partner_org;

DROP INDEX IF EXISTS idx_rel_partner_counterparty;

DROP INDEX IF EXISTS idx_rel_org_people;

DROP INDEX IF EXISTS idx_rel_employer_people;

DROP INDEX IF EXISTS idx_rel_deal_stakeholders;

DROP INDEX IF EXISTS idx_record_grant_subject;

DROP INDEX IF EXISTS idx_record_grant_record;

DROP INDEX IF EXISTS idx_quota_team;

DROP INDEX IF EXISTS idx_quota_owner;

DROP INDEX IF EXISTS idx_project_search;

DROP INDEX IF EXISTS idx_project_owner;

DROP INDEX IF EXISTS idx_project_org_open;

DROP INDEX IF EXISTS idx_project_org;

DROP INDEX IF EXISTS idx_project_name_trgm;

DROP INDEX IF EXISTS idx_project_last_activity_keyset;

DROP INDEX IF EXISTS idx_product_active;

DROP INDEX IF EXISTS idx_pph_project;

DROP INDEX IF EXISTS idx_person_updated_keyset;

DROP INDEX IF EXISTS idx_person_social_person;

DROP INDEX IF EXISTS idx_person_search;

DROP INDEX IF EXISTS idx_person_profile_field;

DROP INDEX IF EXISTS idx_person_phone_person;

DROP INDEX IF EXISTS idx_person_owner;

DROP INDEX IF EXISTS idx_person_name_trgm;

DROP INDEX IF EXISTS idx_person_name_keyset_desc;

DROP INDEX IF EXISTS idx_person_name_keyset;

DROP INDEX IF EXISTS idx_person_merged_into;

DROP INDEX IF EXISTS idx_person_last_activity_keyset;

DROP INDEX IF EXISTS idx_person_from_lead;

DROP INDEX IF EXISTS idx_person_email_person;

DROP INDEX IF EXISTS idx_person_email_correspondence;

DROP INDEX IF EXISTS idx_person_created_keyset;

DROP INDEX IF EXISTS idx_person_channel_identity_person;

DROP INDEX IF EXISTS idx_passport_obo;

DROP INDEX IF EXISTS idx_partner_tier;

DROP INDEX IF EXISTS idx_partner_stage;

DROP INDEX IF EXISTS idx_organization_geocoded;

DROP INDEX IF EXISTS idx_organization_domain_disposition_due;

DROP INDEX IF EXISTS idx_org_updated_keyset;

DROP INDEX IF EXISTS idx_org_search;

DROP INDEX IF EXISTS idx_org_rel_type_org;

DROP INDEX IF EXISTS idx_org_rel_type_cascade;

DROP INDEX IF EXISTS idx_org_parent;

DROP INDEX IF EXISTS idx_org_owner;

DROP INDEX IF EXISTS idx_org_name_trgm;

DROP INDEX IF EXISTS idx_org_name_keyset_desc;

DROP INDEX IF EXISTS idx_org_name_keyset;

DROP INDEX IF EXISTS idx_org_lifecycle;

DROP INDEX IF EXISTS idx_org_legal_name_trgm;

DROP INDEX IF EXISTS idx_org_last_activity_keyset;

DROP INDEX IF EXISTS idx_org_domain_org;

DROP INDEX IF EXISTS idx_org_created_keyset;

DROP INDEX IF EXISTS idx_org_class;

DROP INDEX IF EXISTS idx_oli_offer;

DROP INDEX IF EXISTS idx_offer_template_fk;

DROP INDEX IF EXISTS idx_offer_status;

DROP INDEX IF EXISTS idx_offer_deal;

DROP INDEX IF EXISTS idx_list_member_list;

DROP INDEX IF EXISTS idx_list_member_entity;

DROP INDEX IF EXISTS idx_linkedin_connection_org;

DROP INDEX IF EXISTS idx_linkedin_connection_match;

DROP INDEX IF EXISTS idx_linkedin_connection_email;

DROP INDEX IF EXISTS idx_lead_ws_live;

DROP INDEX IF EXISTS idx_lead_sla_open;

DROP INDEX IF EXISTS idx_lead_search;

DROP INDEX IF EXISTS idx_lead_score_history_series;

DROP INDEX IF EXISTS idx_lead_score;

DROP INDEX IF EXISTS idx_lead_qualified_deal;

DROP INDEX IF EXISTS idx_lead_project;

DROP INDEX IF EXISTS idx_lead_owner;

DROP INDEX IF EXISTS idx_lead_name_trgm;

DROP INDEX IF EXISTS idx_lead_merged_into;

DROP INDEX IF EXISTS idx_lead_manual_signal_lead;

DROP INDEX IF EXISTS idx_lead_linkedin;

DROP INDEX IF EXISTS idx_lead_disqualify_reason;

DROP INDEX IF EXISTS idx_lead_cand_org;

DROP INDEX IF EXISTS idx_idempotency_key_created;

DROP INDEX IF EXISTS idx_graph_edge_user;

DROP INDEX IF EXISTS idx_graph_edge_person;

DROP INDEX IF EXISTS idx_fx_rate_lookup;

DROP INDEX IF EXISTS idx_field_provenance_object;

DROP INDEX IF EXISTS idx_event_outbox_unpublished;

DROP INDEX IF EXISTS idx_dsr_open;

DROP INDEX IF EXISTS idx_dsh_ws_time;

DROP INDEX IF EXISTS idx_dsh_deal;

DROP INDEX IF EXISTS idx_domain_disposition_unevidenced;

DROP INDEX IF EXISTS idx_domain_disposition_admission;

DROP INDEX IF EXISTS idx_dedupe_candidate_open;

DROP INDEX IF EXISTS idx_deal_stalled;

DROP INDEX IF EXISTS idx_deal_stage;

DROP INDEX IF EXISTS idx_deal_search;

DROP INDEX IF EXISTS idx_deal_project;

DROP INDEX IF EXISTS idx_deal_pipeline;

DROP INDEX IF EXISTS idx_deal_partner_attribution;

DROP INDEX IF EXISTS idx_deal_partner;

DROP INDEX IF EXISTS idx_deal_owner;

DROP INDEX IF EXISTS idx_deal_org;

DROP INDEX IF EXISTS idx_deal_name_trgm;

DROP INDEX IF EXISTS idx_deal_forecast_history_deal;

DROP INDEX IF EXISTS idx_deal_close;

DROP INDEX IF EXISTS idx_custom_field_object;

DROP INDEX IF EXISTS idx_consent_event_person;

DROP INDEX IF EXISTS idx_consent_event_lead;

DROP INDEX IF EXISTS idx_consent_doi_token_person;

DROP INDEX IF EXISTS idx_commission_partner;

DROP INDEX IF EXISTS idx_commission_deal;

DROP INDEX IF EXISTS idx_capture_watch_renew;

DROP INDEX IF EXISTS idx_capture_sync_due;

DROP INDEX IF EXISTS idx_capture_pending_counterparty_suppressed;

DROP INDEX IF EXISTS idx_capture_pending_counterparty_noise;

DROP INDEX IF EXISTS idx_capture_pending_counterparty_live;

DROP INDEX IF EXISTS idx_capture_pending_counterparty_due;

DROP INDEX IF EXISTS idx_capture_exclusion_value;

DROP INDEX IF EXISTS idx_capture_connection;

DROP INDEX IF EXISTS idx_capture_backfill_conn;

DROP INDEX IF EXISTS idx_capture_auto_enrich_due;

DROP INDEX IF EXISTS idx_brief_run_user;

DROP INDEX IF EXISTS idx_brief_item_state;

DROP INDEX IF EXISTS idx_brief_item_run;

DROP INDEX IF EXISTS idx_brief_item_deal;

DROP INDEX IF EXISTS idx_booking_page_host;

DROP INDEX IF EXISTS idx_automation_key_live;

DROP INDEX IF EXISTS idx_auth_token_user;

DROP INDEX IF EXISTS idx_auth_token_hash;

DROP INDEX IF EXISTS idx_audit_time;

DROP INDEX IF EXISTS idx_audit_entity;

DROP INDEX IF EXISTS idx_audit_actor;

DROP INDEX IF EXISTS idx_attachment_extraction_latest;

DROP INDEX IF EXISTS idx_attachment_entity;

DROP INDEX IF EXISTS idx_are_decided_by;

DROP INDEX IF EXISTS idx_are_deal;

DROP INDEX IF EXISTS idx_are_activity;

DROP INDEX IF EXISTS idx_approval_target;

DROP INDEX IF EXISTS idx_approval_inbox;

DROP INDEX IF EXISTS idx_approval_expiry_due;

DROP INDEX IF EXISTS idx_approval_bundle;

DROP INDEX IF EXISTS idx_app_user_live;

DROP INDEX IF EXISTS idx_aparticipant_user;

DROP INDEX IF EXISTS idx_aparticipant_person;

DROP INDEX IF EXISTS idx_aparticipant_address;

DROP INDEX IF EXISTS idx_alink_project;

DROP INDEX IF EXISTS idx_alink_person;

DROP INDEX IF EXISTS idx_alink_org;

DROP INDEX IF EXISTS idx_alink_lead;

DROP INDEX IF EXISTS idx_alink_deal;

DROP INDEX IF EXISTS idx_ai_feedback_subject;

DROP INDEX IF EXISTS idx_agent_task_passport;

DROP INDEX IF EXISTS idx_agent_task_expiry;

DROP INDEX IF EXISTS idx_agent_task_approval;

DROP INDEX IF EXISTS idx_agent_run_awaiting;

DROP INDEX IF EXISTS idx_activity_ws_time;

DROP INDEX IF EXISTS idx_activity_unlabeled;

DROP INDEX IF EXISTS idx_activity_thread;

DROP INDEX IF EXISTS idx_activity_tasks;

DROP INDEX IF EXISTS idx_activity_search;

DROP INDEX IF EXISTS idx_activity_restricted_until;

DROP INDEX IF EXISTS idx_activity_reminders;

DROP INDEX IF EXISTS idx_activity_meeting_host;

DROP INDEX IF EXISTS idx_activity_labeled;

DROP INDEX IF EXISTS idx_activity_kind;

DROP INDEX IF EXISTS idx_activity_direction;

DROP INDEX IF EXISTS idx_activity_counterparty_outbound_attested;

DROP INDEX IF EXISTS idx_activity_counterparty_email;

DROP INDEX IF EXISTS idx_activity_channel_thread;

DROP INDEX IF EXISTS idx_aam_subject;

DROP INDEX IF EXISTS finance_payment_invoice_ix;

DROP INDEX IF EXISTS finance_payment_account_ix;

DROP INDEX IF EXISTS finance_invoice_open_ix;

DROP INDEX IF EXISTS finance_invoice_credits_ix;

DROP INDEX IF EXISTS finance_invoice_account_ix;

DROP INDEX IF EXISTS finance_customer_link_organization_ux;

DROP INDEX IF EXISTS finance_customer_link_external_ux;

DROP INDEX IF EXISTS extension_secret_workspace_user;

DROP INDEX IF EXISTS extension_secret_workspace_key;

DROP INDEX IF EXISTS extension_secret_user_key;

DROP INDEX IF EXISTS deal_won_without_contract_ix;

DROP INDEX IF EXISTS conversation_claim_person_ix;

DROP INDEX IF EXISTS conversation_claim_activity_ix;

DROP INDEX IF EXISTS contract_renewal_due_ix;

DROP INDEX IF EXISTS contract_deal_ix;

DROP INDEX IF EXISTS contract_account_ix;

DROP INDEX IF EXISTS consent_qualifying_event_source_unique;

DROP INDEX IF EXISTS consent_qualifying_event_person_ix;

DROP INDEX IF EXISTS comms_outbound_workspace_activity_ix;

DROP INDEX IF EXISTS capture_trace_window;

DROP INDEX IF EXISTS capture_trace_user_window;

DROP INDEX IF EXISTS capture_trace_natural_key;

DROP INDEX IF EXISTS capture_trace_message;

DROP INDEX IF EXISTS capture_trace_counterparty;

DROP INDEX IF EXISTS attachment_external_part_key;

DROP INDEX IF EXISTS attachment_contract_ix;

DROP INDEX IF EXISTS attachment_account_ix;

DROP INDEX IF EXISTS ai_call_ws_time;

DROP INDEX IF EXISTS ai_call_ws_run;

DROP INDEX IF EXISTS ai_call_ws_corr;

DROP INDEX IF EXISTS ai_call_terminal_trace_idx;

DROP INDEX IF EXISTS ai_call_payload_ws_time;

DROP INDEX IF EXISTS ai_call_payload_call;

DROP INDEX IF EXISTS ai_call_logical_idx;

ALTER TABLE IF EXISTS workspace DROP CONSTRAINT IF EXISTS workspace_slug_unique;

ALTER TABLE IF EXISTS workspace DROP CONSTRAINT IF EXISTS workspace_pkey;

ALTER TABLE IF EXISTS workspace_email_domain DROP CONSTRAINT IF EXISTS workspace_email_domain_pkey;

ALTER TABLE IF EXISTS workflow_run DROP CONSTRAINT IF EXISTS workflow_run_unique;

ALTER TABLE IF EXISTS workflow_run DROP CONSTRAINT IF EXISTS workflow_run_pkey;

ALTER TABLE IF EXISTS webhook_subscription DROP CONSTRAINT IF EXISTS webhook_subscription_pkey;

ALTER TABLE IF EXISTS webhook_delivery DROP CONSTRAINT IF EXISTS webhook_delivery_pkey;

ALTER TABLE IF EXISTS webhook_delivery DROP CONSTRAINT IF EXISTS webhook_delivery_dedupe_key;

ALTER TABLE IF EXISTS voice_profile_version DROP CONSTRAINT IF EXISTS voice_profile_version_pkey;

ALTER TABLE IF EXISTS voice_profile DROP CONSTRAINT IF EXISTS voice_profile_pkey;

ALTER TABLE IF EXISTS voice_profile_delta DROP CONSTRAINT IF EXISTS voice_profile_delta_pkey;

ALTER TABLE IF EXISTS voice_learning_signal DROP CONSTRAINT IF EXISTS voice_learning_signal_pkey;

ALTER TABLE IF EXISTS voice_corpus_source DROP CONSTRAINT IF EXISTS voice_corpus_source_pkey;

ALTER TABLE IF EXISTS voice_build DROP CONSTRAINT IF EXISTS voice_build_pkey;

ALTER TABLE IF EXISTS vault_secret DROP CONSTRAINT IF EXISTS vault_secret_pkey;

ALTER TABLE IF EXISTS user_record_view DROP CONSTRAINT IF EXISTS user_record_view_user_id_entity_type_entity_id_key;

ALTER TABLE IF EXISTS user_record_view DROP CONSTRAINT IF EXISTS user_record_view_pkey;

ALTER TABLE IF EXISTS voice_profile_version DROP CONSTRAINT IF EXISTS uq_voice_profile_version_profile_number;

ALTER TABLE IF EXISTS voice_profile_version DROP CONSTRAINT IF EXISTS uq_voice_profile_version_number;

ALTER TABLE IF EXISTS voice_profile_delta DROP CONSTRAINT IF EXISTS uq_voice_profile_delta_version;

ALTER TABLE IF EXISTS voice_learning_signal DROP CONSTRAINT IF EXISTS uq_voice_learning_signal_draft;

ALTER TABLE IF EXISTS voice_corpus_source DROP CONSTRAINT IF EXISTS uq_voice_corpus_source_ref;

ALTER TABLE IF EXISTS site_read DROP CONSTRAINT IF EXISTS uq_site_read_ws_id;

ALTER TABLE IF EXISTS project DROP CONSTRAINT IF EXISTS uq_project_ws_id;

ALTER TABLE IF EXISTS product DROP CONSTRAINT IF EXISTS uq_product_ws_id;

ALTER TABLE IF EXISTS person_profile_field DROP CONSTRAINT IF EXISTS uq_person_profile_field;

ALTER TABLE IF EXISTS passport DROP CONSTRAINT IF EXISTS uq_passport_ws_id;

ALTER TABLE IF EXISTS organization_profile_field DROP CONSTRAINT IF EXISTS uq_org_profile_field;

ALTER TABLE IF EXISTS organization_fact DROP CONSTRAINT IF EXISTS uq_org_fact;

ALTER TABLE IF EXISTS offer_line_item DROP CONSTRAINT IF EXISTS uq_oli_position;

ALTER TABLE IF EXISTS offer DROP CONSTRAINT IF EXISTS uq_offer_ws_id;

ALTER TABLE IF EXISTS offer_template DROP CONSTRAINT IF EXISTS uq_offer_template_ws_id;

ALTER TABLE IF EXISTS lead DROP CONSTRAINT IF EXISTS uq_lead_ws_id;

ALTER TABLE IF EXISTS capture_connection DROP CONSTRAINT IF EXISTS uq_capture_connection_ws_id;

ALTER TABLE IF EXISTS brief_item DROP CONSTRAINT IF EXISTS uq_brief_item_run_rank;

ALTER TABLE IF EXISTS brief_item DROP CONSTRAINT IF EXISTS uq_brief_item_run_deal;

ALTER TABLE IF EXISTS ai_call DROP CONSTRAINT IF EXISTS uq_ai_call_ws_id;

ALTER TABLE IF EXISTS transcript_read DROP CONSTRAINT IF EXISTS transcript_read_pkey;

ALTER TABLE IF EXISTS team DROP CONSTRAINT IF EXISTS team_pkey;

ALTER TABLE IF EXISTS team DROP CONSTRAINT IF EXISTS team_name_unique;

ALTER TABLE IF EXISTS team_membership DROP CONSTRAINT IF EXISTS team_membership_unique;

ALTER TABLE IF EXISTS team_membership DROP CONSTRAINT IF EXISTS team_membership_pkey;

ALTER TABLE IF EXISTS taggable DROP CONSTRAINT IF EXISTS taggable_unique;

ALTER TABLE IF EXISTS taggable DROP CONSTRAINT IF EXISTS taggable_pkey;

ALTER TABLE IF EXISTS tag DROP CONSTRAINT IF EXISTS tag_pkey;

ALTER TABLE IF EXISTS system_log DROP CONSTRAINT IF EXISTS system_log_pkey;

ALTER TABLE IF EXISTS suggestion_dismissal DROP CONSTRAINT IF EXISTS suggestion_dismissal_user_id_organization_id_f_key;

ALTER TABLE IF EXISTS suggestion_dismissal DROP CONSTRAINT IF EXISTS suggestion_dismissal_pkey;

ALTER TABLE IF EXISTS stage DROP CONSTRAINT IF EXISTS stage_pkey;

ALTER TABLE IF EXISTS stage DROP CONSTRAINT IF EXISTS stage_id_pipeline_unique;

ALTER TABLE IF EXISTS site_read DROP CONSTRAINT IF EXISTS site_read_pkey;

ALTER TABLE IF EXISTS signing_key DROP CONSTRAINT IF EXISTS signing_key_pkey;

ALTER TABLE IF EXISTS signal_thread_scan DROP CONSTRAINT IF EXISTS signal_thread_scan_pkey;

ALTER TABLE IF EXISTS signal_resolution DROP CONSTRAINT IF EXISTS signal_resolution_pkey;

ALTER TABLE IF EXISTS signal DROP CONSTRAINT IF EXISTS signal_pkey;

ALTER TABLE IF EXISTS setup_token DROP CONSTRAINT IF EXISTS setup_token_token_hash_key;

ALTER TABLE IF EXISTS setup_token DROP CONSTRAINT IF EXISTS setup_token_pkey;

ALTER TABLE IF EXISTS setting DROP CONSTRAINT IF EXISTS setting_pkey;

ALTER TABLE IF EXISTS session DROP CONSTRAINT IF EXISTS session_token_hash_key;

ALTER TABLE IF EXISTS session DROP CONSTRAINT IF EXISTS session_pkey;

ALTER TABLE IF EXISTS scheduled_send DROP CONSTRAINT IF EXISTS scheduled_send_pkey;

ALTER TABLE IF EXISTS saved_view DROP CONSTRAINT IF EXISTS saved_view_pkey;

ALTER TABLE IF EXISTS runner_job DROP CONSTRAINT IF EXISTS runner_job_trigger_unique;

ALTER TABLE IF EXISTS runner_job DROP CONSTRAINT IF EXISTS runner_job_pkey;

ALTER TABLE IF EXISTS role DROP CONSTRAINT IF EXISTS role_pkey;

ALTER TABLE IF EXISTS role DROP CONSTRAINT IF EXISTS role_key_unique;

ALTER TABLE IF EXISTS role_assignment DROP CONSTRAINT IF EXISTS role_assignment_pkey;

ALTER TABLE IF EXISTS retention_policy DROP CONSTRAINT IF EXISTS retention_policy_unique;

ALTER TABLE IF EXISTS retention_policy DROP CONSTRAINT IF EXISTS retention_policy_pkey;

ALTER TABLE IF EXISTS relationship DROP CONSTRAINT IF EXISTS relationship_pkey;

ALTER TABLE IF EXISTS record_grant DROP CONSTRAINT IF EXISTS record_grant_unique;

ALTER TABLE IF EXISTS record_grant DROP CONSTRAINT IF EXISTS record_grant_pkey;

ALTER TABLE IF EXISTS raw_capture DROP CONSTRAINT IF EXISTS raw_capture_source_unique;

ALTER TABLE IF EXISTS raw_capture DROP CONSTRAINT IF EXISTS raw_capture_pkey;

ALTER TABLE IF EXISTS quota DROP CONSTRAINT IF EXISTS quota_pkey;

ALTER TABLE IF EXISTS provider_run_reservation DROP CONSTRAINT IF EXISTS provider_run_reservation_pkey;

ALTER TABLE IF EXISTS provider_run DROP CONSTRAINT IF EXISTS provider_run_pkey;

ALTER TABLE IF EXISTS provider_run DROP CONSTRAINT IF EXISTS provider_run_external_correlation_id_key;

ALTER TABLE IF EXISTS provider_connection DROP CONSTRAINT IF EXISTS provider_connection_provider_key;

ALTER TABLE IF EXISTS provider_connection DROP CONSTRAINT IF EXISTS provider_connection_pkey;

ALTER TABLE IF EXISTS provider_connection DROP CONSTRAINT IF EXISTS provider_connection_credential_ref_key;

ALTER TABLE IF EXISTS provider_connection_budget DROP CONSTRAINT IF EXISTS provider_connection_budget_pkey;

ALTER TABLE IF EXISTS project DROP CONSTRAINT IF EXISTS project_pkey;

ALTER TABLE IF EXISTS project_phase_history DROP CONSTRAINT IF EXISTS project_phase_history_pkey;

ALTER TABLE IF EXISTS product DROP CONSTRAINT IF EXISTS product_pkey;

ALTER TABLE IF EXISTS preference_token DROP CONSTRAINT IF EXISTS preference_token_token_key;

ALTER TABLE IF EXISTS preference_token DROP CONSTRAINT IF EXISTS preference_token_pkey;

ALTER TABLE IF EXISTS pipeline DROP CONSTRAINT IF EXISTS pipeline_pkey;

ALTER TABLE IF EXISTS pipeline DROP CONSTRAINT IF EXISTS pipeline_name_unique;

ALTER TABLE IF EXISTS person_social DROP CONSTRAINT IF EXISTS person_social_pkey;

ALTER TABLE IF EXISTS person_social DROP CONSTRAINT IF EXISTS person_social_person_id_platform_key;

ALTER TABLE IF EXISTS person_signature_enrich_state DROP CONSTRAINT IF EXISTS person_signature_enrich_state_pkey;

ALTER TABLE IF EXISTS person_provider_claim DROP CONSTRAINT IF EXISTS person_provider_claim_run_id_claim_key_key;

ALTER TABLE IF EXISTS person_provider_claim DROP CONSTRAINT IF EXISTS person_provider_claim_pkey;

ALTER TABLE IF EXISTS person_profile_field DROP CONSTRAINT IF EXISTS person_profile_field_pkey;

ALTER TABLE IF EXISTS person DROP CONSTRAINT IF EXISTS person_pkey;

ALTER TABLE IF EXISTS person_phone DROP CONSTRAINT IF EXISTS person_phone_pkey;

ALTER TABLE IF EXISTS person_moment_dismissal DROP CONSTRAINT IF EXISTS person_moment_dismissal_pkey;

ALTER TABLE IF EXISTS person_email DROP CONSTRAINT IF EXISTS person_email_pkey;

ALTER TABLE IF EXISTS person_consent DROP CONSTRAINT IF EXISTS person_consent_unique;

ALTER TABLE IF EXISTS person_consent DROP CONSTRAINT IF EXISTS person_consent_pkey;

ALTER TABLE IF EXISTS person_consent DROP CONSTRAINT IF EXISTS person_consent_lead_unique;

ALTER TABLE IF EXISTS person_channel_identity DROP CONSTRAINT IF EXISTS person_channel_identity_pkey;

ALTER TABLE IF EXISTS person_brief DROP CONSTRAINT IF EXISTS person_brief_pkey;

ALTER TABLE IF EXISTS passport DROP CONSTRAINT IF EXISTS passport_token_hash_key;

ALTER TABLE IF EXISTS passport DROP CONSTRAINT IF EXISTS passport_pkey;

ALTER TABLE IF EXISTS partner DROP CONSTRAINT IF EXISTS partner_pkey;

ALTER TABLE IF EXISTS partner DROP CONSTRAINT IF EXISTS partner_organization_id_key;

ALTER TABLE IF EXISTS organization_relationship_type DROP CONSTRAINT IF EXISTS organization_relationship_type_pkey;

ALTER TABLE IF EXISTS organization_profile_field DROP CONSTRAINT IF EXISTS organization_profile_field_pkey;

ALTER TABLE IF EXISTS organization DROP CONSTRAINT IF EXISTS organization_pkey;

ALTER TABLE IF EXISTS organization_geocode_state DROP CONSTRAINT IF EXISTS organization_geocode_state_pkey;

ALTER TABLE IF EXISTS organization_fact DROP CONSTRAINT IF EXISTS organization_fact_pkey;

ALTER TABLE IF EXISTS organization_domain DROP CONSTRAINT IF EXISTS organization_domain_pkey;

ALTER TABLE IF EXISTS organization_domain_disposition DROP CONSTRAINT IF EXISTS organization_domain_disposition_pkey;

ALTER TABLE IF EXISTS org_growth_fit DROP CONSTRAINT IF EXISTS org_growth_fit_pkey;

ALTER TABLE IF EXISTS org_dossier DROP CONSTRAINT IF EXISTS org_dossier_pkey;

ALTER TABLE IF EXISTS org_brief DROP CONSTRAINT IF EXISTS org_brief_user_id_organization_id_key;

ALTER TABLE IF EXISTS org_brief DROP CONSTRAINT IF EXISTS org_brief_pkey;

ALTER TABLE IF EXISTS onboarding_wizard_state DROP CONSTRAINT IF EXISTS onboarding_wizard_state_user_id_key;

ALTER TABLE IF EXISTS onboarding_wizard_state DROP CONSTRAINT IF EXISTS onboarding_wizard_state_pkey;

ALTER TABLE IF EXISTS offer_template DROP CONSTRAINT IF EXISTS offer_template_pkey;

ALTER TABLE IF EXISTS offer_template DROP CONSTRAINT IF EXISTS offer_template_name_unique;

ALTER TABLE IF EXISTS offer DROP CONSTRAINT IF EXISTS offer_pkey;

ALTER TABLE IF EXISTS offer DROP CONSTRAINT IF EXISTS offer_number_rev_unique;

ALTER TABLE IF EXISTS offer_line_item DROP CONSTRAINT IF EXISTS offer_line_item_pkey;

ALTER TABLE IF EXISTS oauth_refresh_token DROP CONSTRAINT IF EXISTS oauth_refresh_unique;

ALTER TABLE IF EXISTS oauth_refresh_token DROP CONSTRAINT IF EXISTS oauth_refresh_token_pkey;

ALTER TABLE IF EXISTS oauth_grant DROP CONSTRAINT IF EXISTS oauth_grant_ws_id_key;

ALTER TABLE IF EXISTS oauth_grant DROP CONSTRAINT IF EXISTS oauth_grant_pkey;

ALTER TABLE IF EXISTS oauth_authorization_code DROP CONSTRAINT IF EXISTS oauth_code_unique;

ALTER TABLE IF EXISTS oauth_client DROP CONSTRAINT IF EXISTS oauth_client_unique;

ALTER TABLE IF EXISTS oauth_client DROP CONSTRAINT IF EXISTS oauth_client_pkey;

ALTER TABLE IF EXISTS oauth_client DROP CONSTRAINT IF EXISTS oauth_client_client_id_key;

ALTER TABLE IF EXISTS oauth_authorization_code DROP CONSTRAINT IF EXISTS oauth_authorization_code_pkey;

ALTER TABLE IF EXISTS list DROP CONSTRAINT IF EXISTS list_pkey;

ALTER TABLE IF EXISTS list_member DROP CONSTRAINT IF EXISTS list_member_unique;

ALTER TABLE IF EXISTS list_member DROP CONSTRAINT IF EXISTS list_member_pkey;

ALTER TABLE IF EXISTS linkedin_connection DROP CONSTRAINT IF EXISTS linkedin_connection_pkey;

ALTER TABLE IF EXISTS linkedin_account DROP CONSTRAINT IF EXISTS linkedin_account_pkey;

ALTER TABLE IF EXISTS lead_source DROP CONSTRAINT IF EXISTS lead_source_pkey;

ALTER TABLE IF EXISTS lead_score_history DROP CONSTRAINT IF EXISTS lead_score_history_pkey;

ALTER TABLE IF EXISTS lead DROP CONSTRAINT IF EXISTS lead_pkey;

ALTER TABLE IF EXISTS lead_manual_signal DROP CONSTRAINT IF EXISTS lead_manual_signal_pkey;

ALTER TABLE IF EXISTS lead_disqualify_reason DROP CONSTRAINT IF EXISTS lead_disqualify_reason_pkey;

ALTER TABLE IF EXISTS idempotency_key DROP CONSTRAINT IF EXISTS idempotency_key_pkey;

ALTER TABLE IF EXISTS graph_interaction_edge DROP CONSTRAINT IF EXISTS graph_interaction_edge_pkey;

ALTER TABLE IF EXISTS geocode_cache DROP CONSTRAINT IF EXISTS geocode_cache_pkey;

ALTER TABLE IF EXISTS fx_rate DROP CONSTRAINT IF EXISTS fx_rate_pkey;

ALTER TABLE IF EXISTS fx_rate DROP CONSTRAINT IF EXISTS fx_rate_pair_day;

ALTER TABLE IF EXISTS finance_payment DROP CONSTRAINT IF EXISTS finance_payment_pkey;

ALTER TABLE IF EXISTS finance_payment DROP CONSTRAINT IF EXISTS finance_payment_connection_id_external_id_key;

ALTER TABLE IF EXISTS finance_invoice DROP CONSTRAINT IF EXISTS finance_invoice_pkey;

ALTER TABLE IF EXISTS finance_invoice DROP CONSTRAINT IF EXISTS finance_invoice_connection_id_external_id_key;

ALTER TABLE IF EXISTS finance_external_customer DROP CONSTRAINT IF EXISTS finance_external_customer_pkey;

ALTER TABLE IF EXISTS finance_external_customer DROP CONSTRAINT IF EXISTS finance_external_customer_connection_id_extern_key;

ALTER TABLE IF EXISTS finance_customer_link DROP CONSTRAINT IF EXISTS finance_customer_link_pkey;

ALTER TABLE IF EXISTS finance_connection DROP CONSTRAINT IF EXISTS finance_connection_pkey;

ALTER TABLE IF EXISTS field_provenance DROP CONSTRAINT IF EXISTS field_provenance_pkey;

ALTER TABLE IF EXISTS field_mask DROP CONSTRAINT IF EXISTS field_mask_role_key_object_field_key;

ALTER TABLE IF EXISTS field_mask DROP CONSTRAINT IF EXISTS field_mask_pkey;

ALTER TABLE IF EXISTS extension_secret DROP CONSTRAINT IF EXISTS extension_secret_pkey;

ALTER TABLE IF EXISTS event_outbox DROP CONSTRAINT IF EXISTS event_outbox_pkey;

ALTER TABLE IF EXISTS erasure_suppression DROP CONSTRAINT IF EXISTS erasure_suppression_pkey;

ALTER TABLE IF EXISTS embedding DROP CONSTRAINT IF EXISTS embedding_pkey;

ALTER TABLE IF EXISTS embed_store_binding DROP CONSTRAINT IF EXISTS embed_store_binding_pkey;

ALTER TABLE IF EXISTS email_signature DROP CONSTRAINT IF EXISTS email_signature_pkey;

ALTER TABLE IF EXISTS email_signature DROP CONSTRAINT IF EXISTS email_signature_owner_id_key;

ALTER TABLE IF EXISTS dedupe_candidate DROP CONSTRAINT IF EXISTS dedupe_candidate_pkey;

ALTER TABLE IF EXISTS deal_stage_history DROP CONSTRAINT IF EXISTS deal_stage_history_pkey;

ALTER TABLE IF EXISTS deal DROP CONSTRAINT IF EXISTS deal_pkey;

ALTER TABLE IF EXISTS deal_forecast_history DROP CONSTRAINT IF EXISTS deal_forecast_history_pkey;

ALTER TABLE IF EXISTS data_subject_request DROP CONSTRAINT IF EXISTS data_subject_request_pkey;

ALTER TABLE IF EXISTS custom_field DROP CONSTRAINT IF EXISTS custom_field_pkey;

ALTER TABLE IF EXISTS conversation_claim DROP CONSTRAINT IF EXISTS conversation_claim_pkey;

ALTER TABLE IF EXISTS contract DROP CONSTRAINT IF EXISTS contract_pkey;

ALTER TABLE IF EXISTS consent_qualifying_event DROP CONSTRAINT IF EXISTS consent_qualifying_event_pkey;

ALTER TABLE IF EXISTS consent_purpose DROP CONSTRAINT IF EXISTS consent_purpose_pkey;

ALTER TABLE IF EXISTS consent_purpose DROP CONSTRAINT IF EXISTS consent_purpose_key_unique;

ALTER TABLE IF EXISTS consent_existing_customer_flag DROP CONSTRAINT IF EXISTS consent_existing_customer_flag_pkey;

ALTER TABLE IF EXISTS consent_event DROP CONSTRAINT IF EXISTS consent_event_pkey;

ALTER TABLE IF EXISTS consent_doi_token DROP CONSTRAINT IF EXISTS consent_doi_token_pkey;

ALTER TABLE IF EXISTS consent_doi_token DROP CONSTRAINT IF EXISTS consent_doi_token_hash_unique;

ALTER TABLE IF EXISTS capture_connection DROP CONSTRAINT IF EXISTS connector_connection_pkey;

ALTER TABLE IF EXISTS comms_outbound DROP CONSTRAINT IF EXISTS comms_outbound_pkey;

ALTER TABLE IF EXISTS comms_outbound DROP CONSTRAINT IF EXISTS comms_outbound_message_unique;

ALTER TABLE IF EXISTS commission_entry DROP CONSTRAINT IF EXISTS commission_entry_pkey;

ALTER TABLE IF EXISTS channel_provider DROP CONSTRAINT IF EXISTS channel_provider_pkey;

ALTER TABLE IF EXISTS channel_connection DROP CONSTRAINT IF EXISTS channel_connection_pkey;

ALTER TABLE IF EXISTS capture_trace DROP CONSTRAINT IF EXISTS capture_trace_pkey;

ALTER TABLE IF EXISTS capture_sync_state DROP CONSTRAINT IF EXISTS capture_sync_state_pkey;

ALTER TABLE IF EXISTS capture_pending_counterparty DROP CONSTRAINT IF EXISTS capture_pending_counterparty_pkey;

ALTER TABLE IF EXISTS capture_freemail_domain DROP CONSTRAINT IF EXISTS capture_freemail_domain_pkey;

ALTER TABLE IF EXISTS capture_exclusion DROP CONSTRAINT IF EXISTS capture_exclusion_pkey;

ALTER TABLE IF EXISTS capture_digest DROP CONSTRAINT IF EXISTS capture_digest_user_id_digest_date_key;

ALTER TABLE IF EXISTS capture_digest DROP CONSTRAINT IF EXISTS capture_digest_pkey;

ALTER TABLE IF EXISTS capture_connection DROP CONSTRAINT IF EXISTS capture_connection_unique;

ALTER TABLE IF EXISTS capture_backfill DROP CONSTRAINT IF EXISTS capture_backfill_pkey;

ALTER TABLE IF EXISTS capture_auto_enrich_state DROP CONSTRAINT IF EXISTS capture_auto_enrich_state_pkey;

ALTER TABLE IF EXISTS capture_auto_enrich_budget DROP CONSTRAINT IF EXISTS capture_auto_enrich_budget_pkey;

ALTER TABLE IF EXISTS brief_run DROP CONSTRAINT IF EXISTS brief_run_pkey;

ALTER TABLE IF EXISTS brief_item DROP CONSTRAINT IF EXISTS brief_item_pkey;

ALTER TABLE IF EXISTS booking_page DROP CONSTRAINT IF EXISTS booking_page_slug_key;

ALTER TABLE IF EXISTS booking_page DROP CONSTRAINT IF EXISTS booking_page_pkey;

ALTER TABLE IF EXISTS automation DROP CONSTRAINT IF EXISTS automation_pkey;

ALTER TABLE IF EXISTS auth_token DROP CONSTRAINT IF EXISTS auth_token_pkey;

ALTER TABLE IF EXISTS audit_log DROP CONSTRAINT IF EXISTS audit_log_pkey;

ALTER TABLE IF EXISTS attachment DROP CONSTRAINT IF EXISTS attachment_pkey;

ALTER TABLE IF EXISTS attachment_extraction DROP CONSTRAINT IF EXISTS attachment_extraction_pkey;

ALTER TABLE IF EXISTS approval DROP CONSTRAINT IF EXISTS approval_pkey;

ALTER TABLE IF EXISTS app_user DROP CONSTRAINT IF EXISTS app_user_pkey;

ALTER TABLE IF EXISTS ai_usage DROP CONSTRAINT IF EXISTS ai_usage_pkey;

ALTER TABLE IF EXISTS ai_model_rate DROP CONSTRAINT IF EXISTS ai_model_rate_pkey;

ALTER TABLE IF EXISTS ai_model_rate DROP CONSTRAINT IF EXISTS ai_model_rate_key;

ALTER TABLE IF EXISTS ai_feedback DROP CONSTRAINT IF EXISTS ai_feedback_subject_type_subject_id_claim_kind_key;

ALTER TABLE IF EXISTS ai_feedback DROP CONSTRAINT IF EXISTS ai_feedback_pkey;

ALTER TABLE IF EXISTS ai_call DROP CONSTRAINT IF EXISTS ai_call_pkey;

ALTER TABLE IF EXISTS ai_call_payload DROP CONSTRAINT IF EXISTS ai_call_payload_pkey;

ALTER TABLE IF EXISTS ai_call_config DROP CONSTRAINT IF EXISTS ai_call_config_pkey;

ALTER TABLE IF EXISTS agent_task DROP CONSTRAINT IF EXISTS agent_task_pkey;

ALTER TABLE IF EXISTS agent_run DROP CONSTRAINT IF EXISTS agent_run_trigger_unique;

ALTER TABLE IF EXISTS agent_run DROP CONSTRAINT IF EXISTS agent_run_pkey;

ALTER TABLE IF EXISTS activity_retention_evidence DROP CONSTRAINT IF EXISTS activity_retention_evidence_pkey;

ALTER TABLE IF EXISTS activity DROP CONSTRAINT IF EXISTS activity_pkey;

ALTER TABLE IF EXISTS activity_participant_replay DROP CONSTRAINT IF EXISTS activity_participant_replay_pkey;

ALTER TABLE IF EXISTS activity_participant DROP CONSTRAINT IF EXISTS activity_participant_pkey;

ALTER TABLE IF EXISTS activity DROP CONSTRAINT IF EXISTS activity_meeting_no_overlap;

ALTER TABLE IF EXISTS activity_link DROP CONSTRAINT IF EXISTS activity_link_pkey;

ALTER TABLE IF EXISTS activity_kind DROP CONSTRAINT IF EXISTS activity_kind_pkey;

ALTER TABLE IF EXISTS activity_audience_member DROP CONSTRAINT IF EXISTS activity_audience_member_pkey;

COMMENT ON TABLE setting IS NULL;

COMMENT ON TABLE person_moment_dismissal IS NULL;

COMMENT ON TABLE person_brief IS NULL;

COMMENT ON COLUMN person.photo_origin IS NULL;

COMMENT ON COLUMN organization_profile_field.verified_at IS NULL;

COMMENT ON COLUMN organization_profile_field.retrieved_at IS NULL;

DROP VIEW IF EXISTS organization_open_pipeline_rollup;

COMMENT ON COLUMN organization_fact.verified_at IS NULL;

COMMENT ON COLUMN organization_fact.retrieved_at IS NULL;

COMMENT ON COLUMN organization.linkedin_url IS NULL;

COMMENT ON COLUMN organization.classification IS NULL;

COMMENT ON TABLE org_growth_fit IS NULL;

COMMENT ON COLUMN org_dossier.user_id IS NULL;

COMMENT ON TABLE org_dossier IS NULL;

COMMENT ON COLUMN extension_secret.vault_ref IS NULL;

COMMENT ON COLUMN deal.partner_attribution IS NULL;

COMMENT ON COLUMN deal.won_without_contract_reason IS NULL;

COMMENT ON COLUMN conversation_claim.evidence_fingerprint IS NULL;

COMMENT ON TABLE conversation_claim IS NULL;

COMMENT ON COLUMN contract.captured_by IS NULL;

COMMENT ON TABLE consent_qualifying_event IS NULL;

COMMENT ON COLUMN consent_purpose.class IS NULL;

COMMENT ON TABLE consent_existing_customer_flag IS NULL;

COMMENT ON COLUMN consent_event.issuance_trigger IS NULL;

COMMENT ON COLUMN comms_outbound.attachments IS NULL;

COMMENT ON TABLE commission_entry IS NULL;

COMMENT ON COLUMN capture_pending_counterparty.kind IS NULL;

COMMENT ON COLUMN attachment.contract_id IS NULL;

COMMENT ON COLUMN attachment.organization_id IS NULL;

COMMENT ON COLUMN attachment.doc_state IS NULL;

COMMENT ON SCHEMA ext IS NULL;

DELETE FROM lead_source;

DELETE FROM field_mask;

DELETE FROM lead_disqualify_reason;

DELETE FROM channel_provider;

DELETE FROM activity_kind;

SELECT setval('public.event_outbox_seq_seq', 1, false);

DROP TABLE IF EXISTS webhook_subscription CASCADE;

DROP TABLE IF EXISTS webhook_delivery CASCADE;

DROP TABLE IF EXISTS signal_resolution CASCADE;

DROP TABLE IF EXISTS signal CASCADE;

DROP TABLE IF EXISTS graph_interaction_edge CASCADE;

DROP TABLE IF EXISTS embedding CASCADE;

DROP TABLE IF EXISTS embed_store_binding CASCADE;

DROP TABLE IF EXISTS quota CASCADE;

DROP TABLE IF EXISTS retention_policy CASCADE;

DROP TABLE IF EXISTS erasure_suppression CASCADE;

DROP TABLE IF EXISTS vault_secret CASCADE;

DROP TABLE IF EXISTS user_record_view CASCADE;

DROP TABLE IF EXISTS system_log CASCADE;

DROP TABLE IF EXISTS suggestion_dismissal CASCADE;

DROP TABLE IF EXISTS signal_thread_scan CASCADE;

DROP TABLE IF EXISTS setting CASCADE;

DROP TABLE IF EXISTS person_moment_dismissal CASCADE;

DROP TABLE IF EXISTS person_brief CASCADE;

DROP TABLE IF EXISTS org_growth_fit CASCADE;

DROP TABLE IF EXISTS org_dossier CASCADE;

DROP TABLE IF EXISTS org_brief CASCADE;

DROP TABLE IF EXISTS idempotency_key CASCADE;

DROP TABLE IF EXISTS field_provenance CASCADE;

DROP TABLE IF EXISTS extension_secret CASCADE;

DROP TABLE IF EXISTS event_outbox CASCADE;

DROP TABLE IF EXISTS channel_provider CASCADE;

DROP TABLE IF EXISTS brief_run CASCADE;

DROP TABLE IF EXISTS brief_item CASCADE;

DROP TABLE IF EXISTS audit_log CASCADE;

DROP TABLE IF EXISTS agent_task CASCADE;

DROP TABLE IF EXISTS activity_participant_replay CASCADE;

DROP TABLE IF EXISTS activity_kind CASCADE;

DROP TABLE IF EXISTS site_read CASCADE;

DROP TABLE IF EXISTS relationship CASCADE;

DROP TABLE IF EXISTS person_social CASCADE;

DROP TABLE IF EXISTS person_signature_enrich_state CASCADE;

DROP TABLE IF EXISTS person_provider_claim CASCADE;

DROP TABLE IF EXISTS person_profile_field CASCADE;

DROP TABLE IF EXISTS person_phone CASCADE;

DROP TABLE IF EXISTS person_email CASCADE;

DROP TABLE IF EXISTS person_channel_identity CASCADE;

DROP TABLE IF EXISTS person CASCADE;

DROP TABLE IF EXISTS partner CASCADE;

DROP TABLE IF EXISTS organization_relationship_type CASCADE;

DROP TABLE IF EXISTS organization_profile_field CASCADE;

DROP TABLE IF EXISTS organization_geocode_state CASCADE;

DROP TABLE IF EXISTS organization_fact CASCADE;

DROP TABLE IF EXISTS organization_domain_disposition CASCADE;

DROP TABLE IF EXISTS organization_domain CASCADE;

DROP TABLE IF EXISTS organization CASCADE;

DROP TABLE IF EXISTS linkedin_connection CASCADE;

DROP TABLE IF EXISTS linkedin_account CASCADE;

DROP TABLE IF EXISTS lead_source CASCADE;

DROP TABLE IF EXISTS lead_score_history CASCADE;

DROP TABLE IF EXISTS lead_manual_signal CASCADE;

DROP TABLE IF EXISTS lead_disqualify_reason CASCADE;

DROP TABLE IF EXISTS lead CASCADE;

DROP TABLE IF EXISTS geocode_cache CASCADE;

DROP TABLE IF EXISTS email_signature CASCADE;

DROP TABLE IF EXISTS dedupe_candidate CASCADE;

DROP TABLE IF EXISTS conversation_claim CASCADE;

DROP TABLE IF EXISTS provider_run_reservation CASCADE;

DROP TABLE IF EXISTS provider_run CASCADE;

DROP TABLE IF EXISTS provider_connection_budget CASCADE;

DROP TABLE IF EXISTS provider_connection CASCADE;

DROP TABLE IF EXISTS workspace CASCADE;

DROP TABLE IF EXISTS team_membership CASCADE;

DROP TABLE IF EXISTS team CASCADE;

DROP TABLE IF EXISTS setup_token CASCADE;

DROP TABLE IF EXISTS session CASCADE;

DROP TABLE IF EXISTS role_assignment CASCADE;

DROP TABLE IF EXISTS role CASCADE;

DROP TABLE IF EXISTS record_grant CASCADE;

DROP TABLE IF EXISTS passport CASCADE;

DROP TABLE IF EXISTS onboarding_wizard_state CASCADE;

DROP TABLE IF EXISTS oauth_refresh_token CASCADE;

DROP TABLE IF EXISTS oauth_grant CASCADE;

DROP TABLE IF EXISTS oauth_client CASCADE;

DROP TABLE IF EXISTS oauth_authorization_code CASCADE;

DROP TABLE IF EXISTS field_mask CASCADE;

DROP TABLE IF EXISTS auth_token CASCADE;

DROP TABLE IF EXISTS app_user CASCADE;

DROP TABLE IF EXISTS finance_payment CASCADE;

DROP TABLE IF EXISTS finance_invoice CASCADE;

DROP TABLE IF EXISTS finance_external_customer CASCADE;

DROP TABLE IF EXISTS finance_customer_link CASCADE;

DROP TABLE IF EXISTS finance_connection CASCADE;

DROP TABLE IF EXISTS stage CASCADE;

DROP TABLE IF EXISTS project_phase_history CASCADE;

DROP TABLE IF EXISTS project CASCADE;

DROP TABLE IF EXISTS product CASCADE;

DROP TABLE IF EXISTS pipeline CASCADE;

DROP TABLE IF EXISTS offer_template CASCADE;

DROP TABLE IF EXISTS offer_line_item CASCADE;

DROP TABLE IF EXISTS offer CASCADE;

DROP TABLE IF EXISTS fx_rate CASCADE;

DROP TABLE IF EXISTS deal_stage_history CASCADE;

DROP TABLE IF EXISTS deal_forecast_history CASCADE;

DROP TABLE IF EXISTS deal CASCADE;

DROP TABLE IF EXISTS custom_field CASCADE;

DROP TABLE IF EXISTS contract CASCADE;

DROP TABLE IF EXISTS preference_token CASCADE;

DROP TABLE IF EXISTS person_consent CASCADE;

DROP TABLE IF EXISTS data_subject_request CASCADE;

DROP TABLE IF EXISTS consent_qualifying_event CASCADE;

DROP TABLE IF EXISTS consent_purpose CASCADE;

DROP TABLE IF EXISTS consent_existing_customer_flag CASCADE;

DROP TABLE IF EXISTS consent_event CASCADE;

DROP TABLE IF EXISTS consent_doi_token CASCADE;

DROP TABLE IF EXISTS comms_outbound CASCADE;

DROP TABLE IF EXISTS commission_entry CASCADE;

DROP TABLE IF EXISTS taggable CASCADE;

DROP TABLE IF EXISTS tag CASCADE;

DROP TABLE IF EXISTS saved_view CASCADE;

DROP TABLE IF EXISTS list_member CASCADE;

DROP TABLE IF EXISTS list CASCADE;

DROP TABLE IF EXISTS workspace_email_domain CASCADE;

DROP TABLE IF EXISTS raw_capture CASCADE;

DROP TABLE IF EXISTS channel_connection CASCADE;

DROP TABLE IF EXISTS capture_trace CASCADE;

DROP TABLE IF EXISTS capture_sync_state CASCADE;

DROP TABLE IF EXISTS capture_pending_counterparty CASCADE;

DROP TABLE IF EXISTS capture_freemail_domain CASCADE;

DROP TABLE IF EXISTS capture_exclusion CASCADE;

DROP TABLE IF EXISTS capture_digest CASCADE;

DROP TABLE IF EXISTS capture_connection CASCADE;

DROP TABLE IF EXISTS capture_backfill CASCADE;

DROP TABLE IF EXISTS capture_auto_enrich_state CASCADE;

DROP TABLE IF EXISTS capture_auto_enrich_budget CASCADE;

DROP TABLE IF EXISTS workflow_run CASCADE;

DROP TABLE IF EXISTS automation CASCADE;

DROP TABLE IF EXISTS signing_key CASCADE;

DROP TABLE IF EXISTS approval CASCADE;

DROP TABLE IF EXISTS voice_profile_version CASCADE;

DROP TABLE IF EXISTS voice_profile_delta CASCADE;

DROP TABLE IF EXISTS voice_profile CASCADE;

DROP TABLE IF EXISTS voice_learning_signal CASCADE;

DROP TABLE IF EXISTS voice_corpus_source CASCADE;

DROP TABLE IF EXISTS voice_build CASCADE;

DROP TABLE IF EXISTS ai_usage CASCADE;

DROP TABLE IF EXISTS ai_model_rate CASCADE;

DROP TABLE IF EXISTS ai_feedback CASCADE;

DROP TABLE IF EXISTS ai_call_payload CASCADE;

DROP TABLE IF EXISTS ai_call_config CASCADE;

DROP TABLE IF EXISTS ai_call CASCADE;

DROP TABLE IF EXISTS runner_job CASCADE;

DROP TABLE IF EXISTS agent_run CASCADE;

DROP TABLE IF EXISTS transcript_read CASCADE;

DROP TABLE IF EXISTS scheduled_send CASCADE;

DROP TABLE IF EXISTS booking_page CASCADE;

DROP TABLE IF EXISTS attachment_extraction CASCADE;

DROP TABLE IF EXISTS attachment CASCADE;

DROP TABLE IF EXISTS activity_retention_evidence CASCADE;

DROP TABLE IF EXISTS activity_participant CASCADE;

DROP TABLE IF EXISTS activity_link CASCADE;

DROP TABLE IF EXISTS activity_audience_member CASCADE;

DROP TABLE IF EXISTS activity CASCADE;

DROP FUNCTION IF EXISTS trg_relationship_last_activity();

DROP FUNCTION IF EXISTS trg_deal_last_activity();

DROP FUNCTION IF EXISTS trg_activity_project_last_activity();

DROP FUNCTION IF EXISTS trg_activity_last_activity();

DROP FUNCTION IF EXISTS system_log_immutable();

DROP FUNCTION IF EXISTS refresh_last_activity_for_link(pid uuid, did uuid, oid uuid);

DROP FUNCTION IF EXISTS organization_refuse_anchor_retirement();

DROP FUNCTION IF EXISTS organization_no_ancestor_cycle();

DROP FUNCTION IF EXISTS move_project_last_activity(pid uuid);

DROP FUNCTION IF EXISTS move_last_activity(tbl regclass, rid uuid);

DROP FUNCTION IF EXISTS last_activity_of_project(pid uuid);

DROP FUNCTION IF EXISTS last_activity_of_person(pid uuid);

DROP FUNCTION IF EXISTS last_activity_of_organization(oid uuid);

DROP FUNCTION IF EXISTS last_activity_of_deal(did uuid);

DROP FUNCTION IF EXISTS deal_clear_partner_attribution_on_org_delete();

DROP FUNCTION IF EXISTS audit_log_immutable();

DROP FUNCTION IF EXISTS assert_deal_project_same_org();

DROP FUNCTION IF EXISTS activity_retention_evidence_is_frozen();

DROP FUNCTION IF EXISTS activity_refuse_restricted_mutation();

DROP FUNCTION IF EXISTS uuidv7();

DROP FUNCTION IF EXISTS trg_activity_link_project_last_activity();

DROP FUNCTION IF EXISTS trg_activity_link_last_activity();

DROP FUNCTION IF EXISTS set_updated_at_bump_version();

DROP FUNCTION IF EXISTS set_updated_at();

DROP FUNCTION IF EXISTS organization_geocode_goes_stale();

DROP FUNCTION IF EXISTS f_fold_apostrophes(text);

DROP FUNCTION IF EXISTS f_unaccent(text);

DROP FUNCTION IF EXISTS comms_outbound_attachments_well_formed(files jsonb);

DROP FUNCTION IF EXISTS activity_ts_config(lang text);

DROP EXTENSION IF EXISTS vector;

DROP EXTENSION IF EXISTS unaccent;

DROP EXTENSION IF EXISTS pg_trgm;

DROP EXTENSION IF EXISTS btree_gist;

DROP SCHEMA IF EXISTS ext;

