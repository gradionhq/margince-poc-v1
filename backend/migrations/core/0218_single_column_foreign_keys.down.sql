-- Reverse of phase C: the foreign keys go back to their composite form, each
-- restored to the definition pg_get_constraintdef reported before the rewrite.
-- The two uniques the up added are dropped last: they are what it pointed at.

ALTER TABLE webhook_subscription DROP CONSTRAINT webhook_subscription_owner_fkey;
ALTER TABLE webhook_subscription ADD CONSTRAINT webhook_subscription_owner_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE webhook_delivery DROP CONSTRAINT webhook_delivery_subscription_fkey;
ALTER TABLE webhook_delivery ADD CONSTRAINT webhook_delivery_subscription_fkey FOREIGN KEY (workspace_id, subscription_id) REFERENCES webhook_subscription(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE voice_profile_version DROP CONSTRAINT voice_profile_version_profile_fkey;
ALTER TABLE voice_profile_version ADD CONSTRAINT voice_profile_version_profile_fkey FOREIGN KEY (workspace_id, voice_profile_id) REFERENCES voice_profile(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE voice_profile_version DROP CONSTRAINT voice_profile_version_predecessor_fkey;
ALTER TABLE voice_profile_version ADD CONSTRAINT voice_profile_version_predecessor_fkey FOREIGN KEY (workspace_id, voice_profile_id, predecessor_version) REFERENCES voice_profile_version(workspace_id, voice_profile_id, profile_version);

ALTER TABLE voice_profile_delta DROP CONSTRAINT voice_profile_delta_to_fkey;
ALTER TABLE voice_profile_delta ADD CONSTRAINT voice_profile_delta_to_fkey FOREIGN KEY (workspace_id, voice_profile_id, to_version) REFERENCES voice_profile_version(workspace_id, voice_profile_id, profile_version);

ALTER TABLE voice_profile_delta DROP CONSTRAINT voice_profile_delta_profile_fkey;
ALTER TABLE voice_profile_delta ADD CONSTRAINT voice_profile_delta_profile_fkey FOREIGN KEY (workspace_id, voice_profile_id) REFERENCES voice_profile(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE voice_profile_delta DROP CONSTRAINT voice_profile_delta_from_fkey;
ALTER TABLE voice_profile_delta ADD CONSTRAINT voice_profile_delta_from_fkey FOREIGN KEY (workspace_id, voice_profile_id, from_version) REFERENCES voice_profile_version(workspace_id, voice_profile_id, profile_version);

ALTER TABLE voice_profile DROP CONSTRAINT voice_profile_team_fkey;
ALTER TABLE voice_profile ADD CONSTRAINT voice_profile_team_fkey FOREIGN KEY (workspace_id, team_id) REFERENCES team(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE voice_profile DROP CONSTRAINT voice_profile_owner_fkey;
ALTER TABLE voice_profile ADD CONSTRAINT voice_profile_owner_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE voice_learning_signal DROP CONSTRAINT voice_learning_signal_version_fkey;
ALTER TABLE voice_learning_signal ADD CONSTRAINT voice_learning_signal_version_fkey FOREIGN KEY (workspace_id, voice_profile_id, profile_version) REFERENCES voice_profile_version(workspace_id, voice_profile_id, profile_version);

ALTER TABLE voice_learning_signal DROP CONSTRAINT voice_learning_signal_profile_fkey;
ALTER TABLE voice_learning_signal ADD CONSTRAINT voice_learning_signal_profile_fkey FOREIGN KEY (workspace_id, voice_profile_id) REFERENCES voice_profile(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE voice_corpus_source DROP CONSTRAINT voice_corpus_source_profile_fkey;
ALTER TABLE voice_corpus_source ADD CONSTRAINT voice_corpus_source_profile_fkey FOREIGN KEY (workspace_id, voice_profile_id) REFERENCES voice_profile(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE voice_build DROP CONSTRAINT voice_build_result_version_fkey;
ALTER TABLE voice_build ADD CONSTRAINT voice_build_result_version_fkey FOREIGN KEY (workspace_id, voice_profile_id, result_version) REFERENCES voice_profile_version(workspace_id, voice_profile_id, profile_version);

ALTER TABLE voice_build DROP CONSTRAINT voice_build_requester_fkey;
ALTER TABLE voice_build ADD CONSTRAINT voice_build_requester_fkey FOREIGN KEY (workspace_id, requested_by) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (requested_by);

ALTER TABLE voice_build DROP CONSTRAINT voice_build_profile_fkey;
ALTER TABLE voice_build ADD CONSTRAINT voice_build_profile_fkey FOREIGN KEY (workspace_id, voice_profile_id) REFERENCES voice_profile(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE user_record_view DROP CONSTRAINT user_record_view_user_id_fkey;
ALTER TABLE user_record_view ADD CONSTRAINT user_record_view_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE team_membership DROP CONSTRAINT team_membership_user_id_fkey;
ALTER TABLE team_membership ADD CONSTRAINT team_membership_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE team_membership DROP CONSTRAINT team_membership_team_id_fkey;
ALTER TABLE team_membership ADD CONSTRAINT team_membership_team_id_fkey FOREIGN KEY (workspace_id, team_id) REFERENCES team(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE team DROP CONSTRAINT team_parent_team_id_fkey;
ALTER TABLE team ADD CONSTRAINT team_parent_team_id_fkey FOREIGN KEY (workspace_id, parent_team_id) REFERENCES team(workspace_id, id) ON DELETE SET NULL (parent_team_id);

ALTER TABLE taggable DROP CONSTRAINT taggable_tag_id_fkey;
ALTER TABLE taggable ADD CONSTRAINT taggable_tag_id_fkey FOREIGN KEY (workspace_id, tag_id) REFERENCES tag(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE system_log DROP CONSTRAINT system_log_on_behalf_of_fkey;
ALTER TABLE system_log ADD CONSTRAINT system_log_on_behalf_of_fkey FOREIGN KEY (workspace_id, on_behalf_of) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (on_behalf_of);

ALTER TABLE suggestion_dismissal DROP CONSTRAINT suggestion_dismissal_user_id_fkey;
ALTER TABLE suggestion_dismissal ADD CONSTRAINT suggestion_dismissal_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE suggestion_dismissal DROP CONSTRAINT suggestion_dismissal_org_fkey;
ALTER TABLE suggestion_dismissal ADD CONSTRAINT suggestion_dismissal_org_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE stage DROP CONSTRAINT stage_pipeline_id_fkey;
ALTER TABLE stage ADD CONSTRAINT stage_pipeline_id_fkey FOREIGN KEY (workspace_id, pipeline_id) REFERENCES pipeline(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE site_read DROP CONSTRAINT site_read_org_fkey;
ALTER TABLE site_read ADD CONSTRAINT site_read_org_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE signal_thread_scan DROP CONSTRAINT signal_thread_scan_resolved_org_fkey;
ALTER TABLE signal_thread_scan ADD CONSTRAINT signal_thread_scan_resolved_org_fkey FOREIGN KEY (workspace_id, resolved_org_id) REFERENCES organization(workspace_id, id) ON DELETE SET NULL (resolved_org_id);

ALTER TABLE signal_resolution DROP CONSTRAINT sigres_signal_fkey;
ALTER TABLE signal_resolution ADD CONSTRAINT sigres_signal_fkey FOREIGN KEY (workspace_id, signal_id) REFERENCES signal(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE signal_resolution DROP CONSTRAINT sigres_resolved_by_fkey;
ALTER TABLE signal_resolution ADD CONSTRAINT sigres_resolved_by_fkey FOREIGN KEY (workspace_id, resolved_by) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (resolved_by);

ALTER TABLE signal_resolution DROP CONSTRAINT sigres_org_fkey;
ALTER TABLE signal_resolution ADD CONSTRAINT sigres_org_fkey FOREIGN KEY (workspace_id, matched_org_id) REFERENCES organization(workspace_id, id) ON DELETE SET NULL (matched_org_id);

ALTER TABLE signal DROP CONSTRAINT signal_resolved_person_fkey;
ALTER TABLE signal ADD CONSTRAINT signal_resolved_person_fkey FOREIGN KEY (workspace_id, resolved_person_id) REFERENCES person(workspace_id, id) ON DELETE SET NULL (resolved_person_id);

ALTER TABLE signal DROP CONSTRAINT signal_resolved_org_fkey;
ALTER TABLE signal ADD CONSTRAINT signal_resolved_org_fkey FOREIGN KEY (workspace_id, resolved_org_id) REFERENCES organization(workspace_id, id) ON DELETE SET NULL (resolved_org_id);

ALTER TABLE signal DROP CONSTRAINT signal_owner_fkey;
ALTER TABLE signal ADD CONSTRAINT signal_owner_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (owner_id);

ALTER TABLE session DROP CONSTRAINT session_user_id_fkey;
ALTER TABLE session ADD CONSTRAINT session_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE saved_view DROP CONSTRAINT saved_view_owner_fkey;
ALTER TABLE saved_view ADD CONSTRAINT saved_view_owner_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE runner_job DROP CONSTRAINT runner_job_run_fkey;
ALTER TABLE runner_job ADD CONSTRAINT runner_job_run_fkey FOREIGN KEY (workspace_id, agent_run_id) REFERENCES agent_run(workspace_id, id) ON DELETE SET NULL (agent_run_id);

ALTER TABLE runner_job DROP CONSTRAINT runner_job_passport_fkey;
ALTER TABLE runner_job ADD CONSTRAINT runner_job_passport_fkey FOREIGN KEY (workspace_id, passport_id) REFERENCES passport(workspace_id, id) ON DELETE SET NULL (passport_id);

ALTER TABLE role_assignment DROP CONSTRAINT role_assignment_user_id_fkey;
ALTER TABLE role_assignment ADD CONSTRAINT role_assignment_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE role_assignment DROP CONSTRAINT role_assignment_team_id_fkey;
ALTER TABLE role_assignment ADD CONSTRAINT role_assignment_team_id_fkey FOREIGN KEY (workspace_id, team_id) REFERENCES team(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE role_assignment DROP CONSTRAINT role_assignment_role_id_fkey;
ALTER TABLE role_assignment ADD CONSTRAINT role_assignment_role_id_fkey FOREIGN KEY (workspace_id, role_id) REFERENCES role(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE relationship DROP CONSTRAINT relationship_project_id_fkey;
ALTER TABLE relationship ADD CONSTRAINT relationship_project_id_fkey FOREIGN KEY (workspace_id, project_id) REFERENCES project(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE relationship DROP CONSTRAINT relationship_person_id_fkey;
ALTER TABLE relationship ADD CONSTRAINT relationship_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE relationship DROP CONSTRAINT relationship_organization_id_fkey;
ALTER TABLE relationship ADD CONSTRAINT relationship_organization_id_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE relationship DROP CONSTRAINT relationship_deal_id_fkey;
ALTER TABLE relationship ADD CONSTRAINT relationship_deal_id_fkey FOREIGN KEY (workspace_id, deal_id) REFERENCES deal(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE relationship DROP CONSTRAINT relationship_counterparty_org_id_fkey;
ALTER TABLE relationship ADD CONSTRAINT relationship_counterparty_org_id_fkey FOREIGN KEY (workspace_id, counterparty_org_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE record_grant DROP CONSTRAINT record_grant_granted_by_fkey;
ALTER TABLE record_grant ADD CONSTRAINT record_grant_granted_by_fkey FOREIGN KEY (workspace_id, granted_by) REFERENCES app_user(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE quota DROP CONSTRAINT quota_team_id_fkey;
ALTER TABLE quota ADD CONSTRAINT quota_team_id_fkey FOREIGN KEY (workspace_id, team_id) REFERENCES team(workspace_id, id) ON DELETE SET NULL (team_id);

ALTER TABLE quota DROP CONSTRAINT quota_owner_id_fkey;
ALTER TABLE quota ADD CONSTRAINT quota_owner_id_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (owner_id);

ALTER TABLE project_phase_history DROP CONSTRAINT project_phase_history_project_id_fkey;
ALTER TABLE project_phase_history ADD CONSTRAINT project_phase_history_project_id_fkey FOREIGN KEY (workspace_id, project_id) REFERENCES project(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE project DROP CONSTRAINT project_owner_id_fkey;
ALTER TABLE project ADD CONSTRAINT project_owner_id_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (owner_id);

ALTER TABLE project DROP CONSTRAINT project_organization_id_fkey;
ALTER TABLE project ADD CONSTRAINT project_organization_id_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE preference_token DROP CONSTRAINT preference_token_person_fkey;
ALTER TABLE preference_token ADD CONSTRAINT preference_token_person_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person_social DROP CONSTRAINT person_social_person_id_fkey;
ALTER TABLE person_social ADD CONSTRAINT person_social_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person_signature_enrich_state DROP CONSTRAINT person_signature_enrich_state_person_id_fkey;
ALTER TABLE person_signature_enrich_state ADD CONSTRAINT person_signature_enrich_state_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person_signature_enrich_state DROP CONSTRAINT person_signature_enrich_state_activity_id_fkey;
ALTER TABLE person_signature_enrich_state ADD CONSTRAINT person_signature_enrich_state_activity_id_fkey FOREIGN KEY (workspace_id, activity_id) REFERENCES activity(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person_profile_field DROP CONSTRAINT person_profile_field_person_fk;
ALTER TABLE person_profile_field ADD CONSTRAINT person_profile_field_person_fk FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person_phone DROP CONSTRAINT person_phone_person_id_fkey;
ALTER TABLE person_phone ADD CONSTRAINT person_phone_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person_moment_dismissal DROP CONSTRAINT person_moment_dismissal_user_fkey;
ALTER TABLE person_moment_dismissal ADD CONSTRAINT person_moment_dismissal_user_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person_moment_dismissal DROP CONSTRAINT person_moment_dismissal_person_fkey;
ALTER TABLE person_moment_dismissal ADD CONSTRAINT person_moment_dismissal_person_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person_email DROP CONSTRAINT person_email_person_id_fkey;
ALTER TABLE person_email ADD CONSTRAINT person_email_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person_consent DROP CONSTRAINT person_consent_purpose_id_fkey;
ALTER TABLE person_consent ADD CONSTRAINT person_consent_purpose_id_fkey FOREIGN KEY (workspace_id, purpose_id) REFERENCES consent_purpose(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE person_consent DROP CONSTRAINT person_consent_person_id_fkey;
ALTER TABLE person_consent ADD CONSTRAINT person_consent_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person_consent DROP CONSTRAINT person_consent_lead_id_fkey;
ALTER TABLE person_consent ADD CONSTRAINT person_consent_lead_id_fkey FOREIGN KEY (workspace_id, lead_id) REFERENCES lead(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person_channel_identity DROP CONSTRAINT person_channel_identity_person_id_fkey;
ALTER TABLE person_channel_identity ADD CONSTRAINT person_channel_identity_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person_brief DROP CONSTRAINT person_brief_user_fkey;
ALTER TABLE person_brief ADD CONSTRAINT person_brief_user_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person_brief DROP CONSTRAINT person_brief_person_fkey;
ALTER TABLE person_brief ADD CONSTRAINT person_brief_person_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE person DROP CONSTRAINT person_owner_id_fkey;
ALTER TABLE person ADD CONSTRAINT person_owner_id_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (owner_id);

ALTER TABLE person DROP CONSTRAINT person_merged_into_id_fkey;
ALTER TABLE person ADD CONSTRAINT person_merged_into_id_fkey FOREIGN KEY (workspace_id, merged_into_id) REFERENCES person(workspace_id, id) ON DELETE SET NULL (merged_into_id);

ALTER TABLE person DROP CONSTRAINT person_converted_from_lead_id_fkey;
ALTER TABLE person ADD CONSTRAINT person_converted_from_lead_id_fkey FOREIGN KEY (workspace_id, converted_from_lead_id) REFERENCES lead(workspace_id, id) ON DELETE SET NULL (converted_from_lead_id);

ALTER TABLE passport DROP CONSTRAINT passport_on_behalf_of_fkey;
ALTER TABLE passport ADD CONSTRAINT passport_on_behalf_of_fkey FOREIGN KEY (workspace_id, on_behalf_of) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE passport DROP CONSTRAINT passport_granted_by_fkey;
ALTER TABLE passport ADD CONSTRAINT passport_granted_by_fkey FOREIGN KEY (workspace_id, granted_by) REFERENCES app_user(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE passport DROP CONSTRAINT passport_grant_fkey;
ALTER TABLE passport ADD CONSTRAINT passport_grant_fkey FOREIGN KEY (workspace_id, oauth_grant_id) REFERENCES oauth_grant(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE partner DROP CONSTRAINT partner_organization_id_fkey;
ALTER TABLE partner ADD CONSTRAINT partner_organization_id_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE organization_relationship_type DROP CONSTRAINT organization_relationship_typ_workspace_id_organization_id_fkey;
ALTER TABLE organization_relationship_type ADD CONSTRAINT organization_relationship_typ_workspace_id_organization_id_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE organization_profile_field DROP CONSTRAINT org_profile_field_org_fkey;
ALTER TABLE organization_profile_field ADD CONSTRAINT org_profile_field_org_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE organization_fact DROP CONSTRAINT org_fact_site_read_fkey;
ALTER TABLE organization_fact ADD CONSTRAINT org_fact_site_read_fkey FOREIGN KEY (workspace_id, site_read_id) REFERENCES site_read(workspace_id, id) ON DELETE SET NULL (site_read_id);

ALTER TABLE organization_fact DROP CONSTRAINT org_fact_org_fkey;
ALTER TABLE organization_fact ADD CONSTRAINT org_fact_org_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE organization_domain_disposition DROP CONSTRAINT organization_domain_disposition_site_read_fkey;
ALTER TABLE organization_domain_disposition ADD CONSTRAINT organization_domain_disposition_site_read_fkey FOREIGN KEY (workspace_id, site_read_id) REFERENCES site_read(workspace_id, id) ON DELETE SET NULL (site_read_id);

ALTER TABLE organization_domain_disposition DROP CONSTRAINT organization_domain_disposition_owner_fkey;
ALTER TABLE organization_domain_disposition ADD CONSTRAINT organization_domain_disposition_owner_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (owner_id);

ALTER TABLE organization_domain_disposition DROP CONSTRAINT organization_domain_disposition_org_fkey;
ALTER TABLE organization_domain_disposition ADD CONSTRAINT organization_domain_disposition_org_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE organization_domain DROP CONSTRAINT organization_domain_organization_id_fkey;
ALTER TABLE organization_domain ADD CONSTRAINT organization_domain_organization_id_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE organization DROP CONSTRAINT organization_parent_org_id_fkey;
ALTER TABLE organization ADD CONSTRAINT organization_parent_org_id_fkey FOREIGN KEY (workspace_id, parent_org_id) REFERENCES organization(workspace_id, id) ON DELETE SET NULL (parent_org_id);

ALTER TABLE organization DROP CONSTRAINT organization_owner_id_fkey;
ALTER TABLE organization ADD CONSTRAINT organization_owner_id_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (owner_id);

ALTER TABLE organization DROP CONSTRAINT organization_merged_into_id_fkey;
ALTER TABLE organization ADD CONSTRAINT organization_merged_into_id_fkey FOREIGN KEY (workspace_id, merged_into_id) REFERENCES organization(workspace_id, id) ON DELETE SET NULL (merged_into_id);

ALTER TABLE org_growth_fit DROP CONSTRAINT org_growth_fit_user_fkey;
ALTER TABLE org_growth_fit ADD CONSTRAINT org_growth_fit_user_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE org_growth_fit DROP CONSTRAINT org_growth_fit_org_fkey;
ALTER TABLE org_growth_fit ADD CONSTRAINT org_growth_fit_org_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE org_dossier DROP CONSTRAINT org_dossier_user_fkey;
ALTER TABLE org_dossier ADD CONSTRAINT org_dossier_user_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE org_dossier DROP CONSTRAINT org_dossier_org_fkey;
ALTER TABLE org_dossier ADD CONSTRAINT org_dossier_org_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE org_brief DROP CONSTRAINT org_brief_user_id_fkey;
ALTER TABLE org_brief ADD CONSTRAINT org_brief_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE org_brief DROP CONSTRAINT org_brief_org_fkey;
ALTER TABLE org_brief ADD CONSTRAINT org_brief_org_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE onboarding_wizard_state DROP CONSTRAINT onboarding_wizard_state_user_fkey;
ALTER TABLE onboarding_wizard_state ADD CONSTRAINT onboarding_wizard_state_user_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE onboarding_wizard_state DROP CONSTRAINT onboarding_wizard_state_read_fkey;
ALTER TABLE onboarding_wizard_state ADD CONSTRAINT onboarding_wizard_state_read_fkey FOREIGN KEY (workspace_id, site_read_id) REFERENCES site_read(workspace_id, id) ON DELETE SET NULL (site_read_id);

ALTER TABLE offer_line_item DROP CONSTRAINT oli_product_fkey;
ALTER TABLE offer_line_item ADD CONSTRAINT oli_product_fkey FOREIGN KEY (workspace_id, product_id) REFERENCES product(workspace_id, id) ON DELETE SET NULL (product_id);

ALTER TABLE offer_line_item DROP CONSTRAINT oli_offer_fkey;
ALTER TABLE offer_line_item ADD CONSTRAINT oli_offer_fkey FOREIGN KEY (workspace_id, offer_id) REFERENCES offer(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE offer DROP CONSTRAINT offer_template_id_fkey;
ALTER TABLE offer ADD CONSTRAINT offer_template_id_fkey FOREIGN KEY (workspace_id, template_id) REFERENCES offer_template(workspace_id, id) ON DELETE SET NULL (template_id);

ALTER TABLE offer DROP CONSTRAINT offer_deal_fkey;
ALTER TABLE offer ADD CONSTRAINT offer_deal_fkey FOREIGN KEY (workspace_id, deal_id) REFERENCES deal(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE offer DROP CONSTRAINT offer_buyer_org_fkey;
ALTER TABLE offer ADD CONSTRAINT offer_buyer_org_fkey FOREIGN KEY (workspace_id, buyer_org_id) REFERENCES organization(workspace_id, id) ON DELETE SET NULL (buyer_org_id);

ALTER TABLE oauth_refresh_token DROP CONSTRAINT oauth_refresh_grant_fkey;
ALTER TABLE oauth_refresh_token ADD CONSTRAINT oauth_refresh_grant_fkey FOREIGN KEY (workspace_id, grant_id) REFERENCES oauth_grant(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE oauth_grant DROP CONSTRAINT oauth_grant_user_fkey;
ALTER TABLE oauth_grant ADD CONSTRAINT oauth_grant_user_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE oauth_grant DROP CONSTRAINT oauth_grant_lent_passport_fkey;
ALTER TABLE oauth_grant ADD CONSTRAINT oauth_grant_lent_passport_fkey FOREIGN KEY (workspace_id, lent_passport_id) REFERENCES passport(workspace_id, id) ON DELETE SET NULL (lent_passport_id);

ALTER TABLE oauth_grant DROP CONSTRAINT oauth_grant_client_fkey;
ALTER TABLE oauth_grant ADD CONSTRAINT oauth_grant_client_fkey FOREIGN KEY (workspace_id, client_id) REFERENCES oauth_client(workspace_id, client_id) ON DELETE RESTRICT;

ALTER TABLE oauth_authorization_code DROP CONSTRAINT oauth_code_user_fkey;
ALTER TABLE oauth_authorization_code ADD CONSTRAINT oauth_code_user_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE oauth_authorization_code DROP CONSTRAINT oauth_code_lent_passport_fkey;
ALTER TABLE oauth_authorization_code ADD CONSTRAINT oauth_code_lent_passport_fkey FOREIGN KEY (workspace_id, lent_passport_id) REFERENCES passport(workspace_id, id) ON DELETE SET NULL (lent_passport_id);

ALTER TABLE list_member DROP CONSTRAINT list_member_list_id_fkey;
ALTER TABLE list_member ADD CONSTRAINT list_member_list_id_fkey FOREIGN KEY (workspace_id, list_id) REFERENCES list(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE list DROP CONSTRAINT list_team_id_fkey;
ALTER TABLE list ADD CONSTRAINT list_team_id_fkey FOREIGN KEY (workspace_id, team_id) REFERENCES team(workspace_id, id) ON DELETE SET NULL (team_id);

ALTER TABLE list DROP CONSTRAINT list_owner_id_fkey;
ALTER TABLE list ADD CONSTRAINT list_owner_id_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (owner_id);

ALTER TABLE linkedin_connection DROP CONSTRAINT linkedin_connection_workspace_id_owner_user_id_fkey;
ALTER TABLE linkedin_connection ADD CONSTRAINT linkedin_connection_workspace_id_owner_user_id_fkey FOREIGN KEY (workspace_id, owner_user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE linkedin_connection DROP CONSTRAINT linkedin_connection_workspace_id_matched_person_id_fkey;
ALTER TABLE linkedin_connection ADD CONSTRAINT linkedin_connection_workspace_id_matched_person_id_fkey FOREIGN KEY (workspace_id, matched_person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE linkedin_connection DROP CONSTRAINT linkedin_connection_workspace_id_matched_org_id_fkey;
ALTER TABLE linkedin_connection ADD CONSTRAINT linkedin_connection_workspace_id_matched_org_id_fkey FOREIGN KEY (workspace_id, matched_org_id) REFERENCES organization(workspace_id, id) ON DELETE SET NULL (matched_org_id);

ALTER TABLE linkedin_account DROP CONSTRAINT linkedin_account_workspace_id_user_id_fkey;
ALTER TABLE linkedin_account ADD CONSTRAINT linkedin_account_workspace_id_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE lead DROP CONSTRAINT lead_promoted_person_id_fkey;
ALTER TABLE lead ADD CONSTRAINT lead_promoted_person_id_fkey FOREIGN KEY (workspace_id, promoted_person_id) REFERENCES person(workspace_id, id) ON DELETE SET NULL (promoted_person_id);

ALTER TABLE lead DROP CONSTRAINT lead_project_id_fkey;
ALTER TABLE lead ADD CONSTRAINT lead_project_id_fkey FOREIGN KEY (workspace_id, project_id) REFERENCES project(workspace_id, id) ON DELETE SET NULL (project_id);

ALTER TABLE lead DROP CONSTRAINT lead_owner_id_fkey;
ALTER TABLE lead ADD CONSTRAINT lead_owner_id_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (owner_id);

ALTER TABLE graph_interaction_edge DROP CONSTRAINT graph_interaction_edge_workspace_id_user_id_fkey;
ALTER TABLE graph_interaction_edge ADD CONSTRAINT graph_interaction_edge_workspace_id_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE graph_interaction_edge DROP CONSTRAINT graph_interaction_edge_workspace_id_person_id_fkey;
ALTER TABLE graph_interaction_edge ADD CONSTRAINT graph_interaction_edge_workspace_id_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE finance_payment DROP CONSTRAINT finance_payment_organization_fk;
ALTER TABLE finance_payment ADD CONSTRAINT finance_payment_organization_fk FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE finance_payment DROP CONSTRAINT finance_payment_invoice_fk;
ALTER TABLE finance_payment ADD CONSTRAINT finance_payment_invoice_fk FOREIGN KEY (workspace_id, invoice_id) REFERENCES finance_invoice(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE finance_payment DROP CONSTRAINT finance_payment_connection_fk;
ALTER TABLE finance_payment ADD CONSTRAINT finance_payment_connection_fk FOREIGN KEY (workspace_id, connection_id) REFERENCES finance_connection(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE finance_invoice DROP CONSTRAINT finance_invoice_organization_fk;
ALTER TABLE finance_invoice ADD CONSTRAINT finance_invoice_organization_fk FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE finance_invoice DROP CONSTRAINT finance_invoice_credits_fk;
ALTER TABLE finance_invoice ADD CONSTRAINT finance_invoice_credits_fk FOREIGN KEY (workspace_id, credits_invoice_id) REFERENCES finance_invoice(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE finance_invoice DROP CONSTRAINT finance_invoice_connection_fk;
ALTER TABLE finance_invoice ADD CONSTRAINT finance_invoice_connection_fk FOREIGN KEY (workspace_id, connection_id) REFERENCES finance_connection(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE finance_external_customer DROP CONSTRAINT finance_external_customer_connection_fk;
ALTER TABLE finance_external_customer ADD CONSTRAINT finance_external_customer_connection_fk FOREIGN KEY (workspace_id, connection_id) REFERENCES finance_connection(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE finance_customer_link DROP CONSTRAINT finance_customer_link_organization_fk;
ALTER TABLE finance_customer_link ADD CONSTRAINT finance_customer_link_organization_fk FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE finance_customer_link DROP CONSTRAINT finance_customer_link_connection_fk;
ALTER TABLE finance_customer_link ADD CONSTRAINT finance_customer_link_connection_fk FOREIGN KEY (workspace_id, connection_id) REFERENCES finance_connection(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE extension_secret DROP CONSTRAINT extension_secret_workspace_id_user_id_fkey;
ALTER TABLE extension_secret ADD CONSTRAINT extension_secret_workspace_id_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE dedupe_candidate DROP CONSTRAINT dedupe_candidate_right_person_id_fkey;
ALTER TABLE dedupe_candidate ADD CONSTRAINT dedupe_candidate_right_person_id_fkey FOREIGN KEY (workspace_id, right_person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE dedupe_candidate DROP CONSTRAINT dedupe_candidate_right_org_id_fkey;
ALTER TABLE dedupe_candidate ADD CONSTRAINT dedupe_candidate_right_org_id_fkey FOREIGN KEY (workspace_id, right_org_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE dedupe_candidate DROP CONSTRAINT dedupe_candidate_left_person_id_fkey;
ALTER TABLE dedupe_candidate ADD CONSTRAINT dedupe_candidate_left_person_id_fkey FOREIGN KEY (workspace_id, left_person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE dedupe_candidate DROP CONSTRAINT dedupe_candidate_left_org_id_fkey;
ALTER TABLE dedupe_candidate ADD CONSTRAINT dedupe_candidate_left_org_id_fkey FOREIGN KEY (workspace_id, left_org_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE dedupe_candidate DROP CONSTRAINT dedupe_candidate_disposed_by_fkey;
ALTER TABLE dedupe_candidate ADD CONSTRAINT dedupe_candidate_disposed_by_fkey FOREIGN KEY (workspace_id, disposed_by) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (disposed_by);

ALTER TABLE deal_stage_history DROP CONSTRAINT deal_stage_history_to_stage_id_fkey;
ALTER TABLE deal_stage_history ADD CONSTRAINT deal_stage_history_to_stage_id_fkey FOREIGN KEY (workspace_id, to_stage_id) REFERENCES stage(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE deal_stage_history DROP CONSTRAINT deal_stage_history_from_stage_id_fkey;
ALTER TABLE deal_stage_history ADD CONSTRAINT deal_stage_history_from_stage_id_fkey FOREIGN KEY (workspace_id, from_stage_id) REFERENCES stage(workspace_id, id) ON DELETE SET NULL (from_stage_id);

ALTER TABLE deal_stage_history DROP CONSTRAINT deal_stage_history_deal_id_fkey;
ALTER TABLE deal_stage_history ADD CONSTRAINT deal_stage_history_deal_id_fkey FOREIGN KEY (workspace_id, deal_id) REFERENCES deal(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE deal_forecast_history DROP CONSTRAINT deal_forecast_history_deal_id_fkey;
ALTER TABLE deal_forecast_history ADD CONSTRAINT deal_forecast_history_deal_id_fkey FOREIGN KEY (workspace_id, deal_id) REFERENCES deal(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE deal DROP CONSTRAINT deal_stage_in_pipeline;
ALTER TABLE deal ADD CONSTRAINT deal_stage_in_pipeline FOREIGN KEY (workspace_id, stage_id, pipeline_id) REFERENCES stage(workspace_id, id, pipeline_id);

ALTER TABLE deal DROP CONSTRAINT deal_stage_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_stage_id_fkey FOREIGN KEY (workspace_id, stage_id) REFERENCES stage(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE deal DROP CONSTRAINT deal_project_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_project_id_fkey FOREIGN KEY (workspace_id, project_id) REFERENCES project(workspace_id, id) ON DELETE SET NULL (project_id);

ALTER TABLE deal DROP CONSTRAINT deal_pipeline_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_pipeline_id_fkey FOREIGN KEY (workspace_id, pipeline_id) REFERENCES pipeline(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE deal DROP CONSTRAINT deal_partner_org_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_partner_org_id_fkey FOREIGN KEY (workspace_id, partner_org_id) REFERENCES organization(workspace_id, id) ON DELETE SET NULL (partner_org_id);

ALTER TABLE deal DROP CONSTRAINT deal_owner_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_owner_id_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (owner_id);

ALTER TABLE deal DROP CONSTRAINT deal_organization_id_fkey;
ALTER TABLE deal ADD CONSTRAINT deal_organization_id_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE SET NULL (organization_id);

ALTER TABLE data_subject_request DROP CONSTRAINT dsr_assignee_fkey;
ALTER TABLE data_subject_request ADD CONSTRAINT dsr_assignee_fkey FOREIGN KEY (workspace_id, assignee_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (assignee_id);

ALTER TABLE custom_field DROP CONSTRAINT custom_field_created_by_fkey;
ALTER TABLE custom_field ADD CONSTRAINT custom_field_created_by_fkey FOREIGN KEY (workspace_id, created_by) REFERENCES app_user(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE conversation_claim DROP CONSTRAINT conversation_claim_task_fkey;
ALTER TABLE conversation_claim ADD CONSTRAINT conversation_claim_task_fkey FOREIGN KEY (workspace_id, task_activity_id) REFERENCES activity(workspace_id, id) ON DELETE SET NULL;

ALTER TABLE conversation_claim DROP CONSTRAINT conversation_claim_person_fkey;
ALTER TABLE conversation_claim ADD CONSTRAINT conversation_claim_person_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE conversation_claim DROP CONSTRAINT conversation_claim_corrector_fkey;
ALTER TABLE conversation_claim ADD CONSTRAINT conversation_claim_corrector_fkey FOREIGN KEY (workspace_id, corrected_by_user_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL;

ALTER TABLE conversation_claim DROP CONSTRAINT conversation_claim_activity_fkey;
ALTER TABLE conversation_claim ADD CONSTRAINT conversation_claim_activity_fkey FOREIGN KEY (workspace_id, source_activity_id) REFERENCES activity(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE consent_qualifying_event DROP CONSTRAINT consent_qualifying_event_person_fkey;
ALTER TABLE consent_qualifying_event ADD CONSTRAINT consent_qualifying_event_person_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE consent_existing_customer_flag DROP CONSTRAINT consent_existing_customer_setter_fkey;
ALTER TABLE consent_existing_customer_flag ADD CONSTRAINT consent_existing_customer_setter_fkey FOREIGN KEY (workspace_id, set_by_user_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL;

ALTER TABLE consent_existing_customer_flag DROP CONSTRAINT consent_existing_customer_person_fkey;
ALTER TABLE consent_existing_customer_flag ADD CONSTRAINT consent_existing_customer_person_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE consent_event DROP CONSTRAINT consent_event_purpose_id_fkey;
ALTER TABLE consent_event ADD CONSTRAINT consent_event_purpose_id_fkey FOREIGN KEY (workspace_id, purpose_id) REFERENCES consent_purpose(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE consent_event DROP CONSTRAINT consent_event_person_id_fkey;
ALTER TABLE consent_event ADD CONSTRAINT consent_event_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE consent_event DROP CONSTRAINT consent_event_lead_id_fkey;
ALTER TABLE consent_event ADD CONSTRAINT consent_event_lead_id_fkey FOREIGN KEY (workspace_id, lead_id) REFERENCES lead(workspace_id, id);

ALTER TABLE consent_doi_token DROP CONSTRAINT consent_doi_token_purpose_id_fkey;
ALTER TABLE consent_doi_token ADD CONSTRAINT consent_doi_token_purpose_id_fkey FOREIGN KEY (workspace_id, purpose_id) REFERENCES consent_purpose(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE consent_doi_token DROP CONSTRAINT consent_doi_token_person_id_fkey;
ALTER TABLE consent_doi_token ADD CONSTRAINT consent_doi_token_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE comms_outbound DROP CONSTRAINT comms_outbound_user_id_fkey;
ALTER TABLE comms_outbound ADD CONSTRAINT comms_outbound_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE comms_outbound DROP CONSTRAINT comms_outbound_activity_id_fkey;
ALTER TABLE comms_outbound ADD CONSTRAINT comms_outbound_activity_id_fkey FOREIGN KEY (workspace_id, activity_id) REFERENCES activity(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE channel_connection DROP CONSTRAINT channel_connection_connected_by_fkey;
ALTER TABLE channel_connection ADD CONSTRAINT channel_connection_connected_by_fkey FOREIGN KEY (workspace_id, connected_by) REFERENCES app_user(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE capture_sync_state DROP CONSTRAINT capture_sync_state_connection_id_fkey;
ALTER TABLE capture_sync_state ADD CONSTRAINT capture_sync_state_connection_id_fkey FOREIGN KEY (workspace_id, connection_id) REFERENCES capture_connection(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE capture_pending_counterparty DROP CONSTRAINT capture_pending_counterparty_proposal_id_fkey;
ALTER TABLE capture_pending_counterparty ADD CONSTRAINT capture_pending_counterparty_proposal_id_fkey FOREIGN KEY (workspace_id, proposal_id) REFERENCES approval(workspace_id, id) ON DELETE SET NULL;

ALTER TABLE capture_pending_counterparty DROP CONSTRAINT capture_pending_counterparty_owner_id_fkey;
ALTER TABLE capture_pending_counterparty ADD CONSTRAINT capture_pending_counterparty_owner_id_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE capture_pending_counterparty DROP CONSTRAINT capture_pending_counterparty_activity_id_fkey;
ALTER TABLE capture_pending_counterparty ADD CONSTRAINT capture_pending_counterparty_activity_id_fkey FOREIGN KEY (workspace_id, activity_id) REFERENCES activity(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE capture_freemail_domain DROP CONSTRAINT capture_freemail_domain_created_by_fkey;
ALTER TABLE capture_freemail_domain ADD CONSTRAINT capture_freemail_domain_created_by_fkey FOREIGN KEY (workspace_id, created_by) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (created_by);

ALTER TABLE capture_digest DROP CONSTRAINT capture_digest_user_id_fkey;
ALTER TABLE capture_digest ADD CONSTRAINT capture_digest_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE capture_connection DROP CONSTRAINT capture_connection_user_id_fkey;
ALTER TABLE capture_connection ADD CONSTRAINT capture_connection_user_id_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE capture_backfill DROP CONSTRAINT capture_backfill_connection_fkey;
ALTER TABLE capture_backfill ADD CONSTRAINT capture_backfill_connection_fkey FOREIGN KEY (workspace_id, connection_id) REFERENCES capture_connection(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE capture_auto_enrich_state DROP CONSTRAINT capture_auto_enrich_state_organization_id_fkey;
ALTER TABLE capture_auto_enrich_state ADD CONSTRAINT capture_auto_enrich_state_organization_id_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE brief_run DROP CONSTRAINT brief_run_user_fkey;
ALTER TABLE brief_run ADD CONSTRAINT brief_run_user_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE brief_item DROP CONSTRAINT brief_item_run_fkey;
ALTER TABLE brief_item ADD CONSTRAINT brief_item_run_fkey FOREIGN KEY (workspace_id, brief_run_id) REFERENCES brief_run(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE brief_item DROP CONSTRAINT brief_item_deal_fkey;
ALTER TABLE brief_item ADD CONSTRAINT brief_item_deal_fkey FOREIGN KEY (workspace_id, deal_id) REFERENCES deal(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE booking_page DROP CONSTRAINT booking_page_host_fkey;
ALTER TABLE booking_page ADD CONSTRAINT booking_page_host_fkey FOREIGN KEY (workspace_id, host_user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE automation DROP CONSTRAINT automation_owner_fkey;
ALTER TABLE automation ADD CONSTRAINT automation_owner_fkey FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (owner_id);

ALTER TABLE auth_token DROP CONSTRAINT auth_token_user_fkey;
ALTER TABLE auth_token ADD CONSTRAINT auth_token_user_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE audit_log DROP CONSTRAINT audit_log_on_behalf_of_fkey;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_on_behalf_of_fkey FOREIGN KEY (workspace_id, on_behalf_of) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (on_behalf_of);

ALTER TABLE attachment DROP CONSTRAINT attachment_supersedes_fkey;
ALTER TABLE attachment ADD CONSTRAINT attachment_supersedes_fkey FOREIGN KEY (workspace_id, supersedes_id) REFERENCES attachment(workspace_id, id) ON DELETE SET NULL (supersedes_id);

ALTER TABLE approval DROP CONSTRAINT approval_passport_id_fkey;
ALTER TABLE approval ADD CONSTRAINT approval_passport_id_fkey FOREIGN KEY (workspace_id, passport_id) REFERENCES passport(workspace_id, id) ON DELETE SET NULL (passport_id);

ALTER TABLE approval DROP CONSTRAINT approval_on_behalf_of_fkey;
ALTER TABLE approval ADD CONSTRAINT approval_on_behalf_of_fkey FOREIGN KEY (workspace_id, on_behalf_of) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (on_behalf_of);

ALTER TABLE approval DROP CONSTRAINT approval_decided_by_fkey;
ALTER TABLE approval ADD CONSTRAINT approval_decided_by_fkey FOREIGN KEY (workspace_id, decided_by) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (decided_by);

ALTER TABLE ai_call_payload DROP CONSTRAINT ai_call_payload_ai_call_fkey;
ALTER TABLE ai_call_payload ADD CONSTRAINT ai_call_payload_ai_call_fkey FOREIGN KEY (workspace_id, ai_call_id) REFERENCES ai_call(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE agent_task DROP CONSTRAINT agent_task_passport_fk;
ALTER TABLE agent_task ADD CONSTRAINT agent_task_passport_fk FOREIGN KEY (workspace_id, passport_id) REFERENCES passport(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE agent_task DROP CONSTRAINT agent_task_approval_fk;
ALTER TABLE agent_task ADD CONSTRAINT agent_task_approval_fk FOREIGN KEY (workspace_id, approval_id) REFERENCES approval(workspace_id, id) ON DELETE RESTRICT;

ALTER TABLE agent_run DROP CONSTRAINT agent_run_passport_fkey;
ALTER TABLE agent_run ADD CONSTRAINT agent_run_passport_fkey FOREIGN KEY (workspace_id, passport_id) REFERENCES passport(workspace_id, id) ON DELETE SET NULL (passport_id);

ALTER TABLE agent_run DROP CONSTRAINT agent_run_approval_fkey;
ALTER TABLE agent_run ADD CONSTRAINT agent_run_approval_fkey FOREIGN KEY (workspace_id, approval_id) REFERENCES approval(workspace_id, id) ON DELETE SET NULL (approval_id);

ALTER TABLE activity_participant_replay DROP CONSTRAINT activity_participant_replay_activity_fkey;
ALTER TABLE activity_participant_replay ADD CONSTRAINT activity_participant_replay_activity_fkey FOREIGN KEY (workspace_id, activity_id) REFERENCES activity(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE activity_participant DROP CONSTRAINT activity_participant_user_fkey;
ALTER TABLE activity_participant ADD CONSTRAINT activity_participant_user_fkey FOREIGN KEY (workspace_id, user_id) REFERENCES app_user(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE activity_participant DROP CONSTRAINT activity_participant_person_fkey;
ALTER TABLE activity_participant ADD CONSTRAINT activity_participant_person_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE activity_participant DROP CONSTRAINT activity_participant_activity_fkey;
ALTER TABLE activity_participant ADD CONSTRAINT activity_participant_activity_fkey FOREIGN KEY (workspace_id, activity_id) REFERENCES activity(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE activity_link DROP CONSTRAINT activity_link_project_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_project_id_fkey FOREIGN KEY (workspace_id, project_id) REFERENCES project(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE activity_link DROP CONSTRAINT activity_link_person_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_person_id_fkey FOREIGN KEY (workspace_id, person_id) REFERENCES person(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE activity_link DROP CONSTRAINT activity_link_organization_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_organization_id_fkey FOREIGN KEY (workspace_id, organization_id) REFERENCES organization(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE activity_link DROP CONSTRAINT activity_link_lead_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_lead_id_fkey FOREIGN KEY (workspace_id, lead_id) REFERENCES lead(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE activity_link DROP CONSTRAINT activity_link_deal_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_deal_id_fkey FOREIGN KEY (workspace_id, deal_id) REFERENCES deal(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE activity_link DROP CONSTRAINT activity_link_activity_id_fkey;
ALTER TABLE activity_link ADD CONSTRAINT activity_link_activity_id_fkey FOREIGN KEY (workspace_id, activity_id) REFERENCES activity(workspace_id, id) ON DELETE CASCADE;

ALTER TABLE activity DROP CONSTRAINT activity_host_user_id_fkey;
ALTER TABLE activity ADD CONSTRAINT activity_host_user_id_fkey FOREIGN KEY (workspace_id, host_user_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (host_user_id);

ALTER TABLE activity DROP CONSTRAINT activity_assignee_id_fkey;
ALTER TABLE activity ADD CONSTRAINT activity_assignee_id_fkey FOREIGN KEY (workspace_id, assignee_id) REFERENCES app_user(workspace_id, id) ON DELETE SET NULL (assignee_id);

ALTER TABLE voice_profile_version DROP CONSTRAINT uq_voice_profile_version_profile_number;
ALTER TABLE oauth_client DROP CONSTRAINT oauth_client_client_id_key;
