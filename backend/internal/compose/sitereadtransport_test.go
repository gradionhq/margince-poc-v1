// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestDeferredSiteReadMetadataReachesBothWireShapes(t *testing.T) {
	nextAttempt := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	statusCode := "budget_deferred"
	statusDetail := "AI budget reached its current limit. This website read will resume automatically."
	organizationID := ids.New[ids.OrganizationKind]()
	read := people.SiteRead{
		ID:             ids.NewV7(),
		OrganizationID: &organizationID,
		TargetKind:     "organization",
		SeedURL:        "https://acme.example",
		Status:         siteReadStatusDeferred,
		StatusCode:     &statusCode,
		StatusDetail:   &statusDetail,
		NextAttemptAt:  &nextAttempt,
	}

	report := siteReadReport(read)
	if report.Status != crmcontracts.SiteReadReportStatusDeferred ||
		report.StatusCode == nil || *report.StatusCode != crmcontracts.SiteReadReportStatusCodeBudgetDeferred ||
		report.StatusDetail == nil || *report.StatusDetail != statusDetail ||
		report.NextAttemptAt == nil || !report.NextAttemptAt.Equal(nextAttempt) {
		t.Fatalf("deferred organization report lost scheduling metadata: %+v", report)
	}

	company := companySiteRead(read, nil)
	if company.Status != crmcontracts.CompanySiteReadStatusDeferred ||
		company.StatusCode == nil || *company.StatusCode != crmcontracts.CompanySiteReadStatusCodeBudgetDeferred ||
		company.StatusDetail == nil || *company.StatusDetail != statusDetail ||
		company.NextAttemptAt == nil || !company.NextAttemptAt.Equal(nextAttempt) {
		t.Fatalf("deferred onboarding report lost scheduling metadata: %+v", company)
	}
}
