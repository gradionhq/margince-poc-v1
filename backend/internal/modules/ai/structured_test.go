package ai

// The §5.2 retry policy as behavior: valid output passes through, an
// invalid first answer retries once WITH the validator's reason, a
// second failure escalates one tier, and total failure is an honest
// error — never a partial fabrication handed to the caller.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

func jsonObjectValidator(text string) error {
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return errors.New("not a JSON object")
	}
	return nil
}

func structuredReq() model.Request {
	return model.Request{Messages: []model.Message{{Role: "user", Content: "extract"}}}
}

func TestStructuredValidFirstTry(t *testing.T) {
	cheap := NewFakeClient().Script(`{"ok":true}`)
	r := testRouter(map[Tier]model.Client{TierCheapCloud: cheap}, &memMeter{}, DefaultMonthlyTokens, ProfileEUHosted)

	resp, _, err := r.CompleteStructured(wsContext(t), TaskColdStart, structuredReq(), jsonObjectValidator)
	if err != nil || resp.Text != `{"ok":true}` {
		t.Fatalf("valid first answer: %v %q", err, resp.Text)
	}
	if calls := cheap.Calls(); len(calls) != 1 {
		t.Fatalf("valid answer cost %d calls, want 1", len(calls))
	}
}

func TestStructuredRetryCarriesValidatorError(t *testing.T) {
	cheap := NewFakeClient().Script("garbage", `{"fixed":true}`)
	r := testRouter(map[Tier]model.Client{TierCheapCloud: cheap}, &memMeter{}, DefaultMonthlyTokens, ProfileEUHosted)

	resp, _, err := r.CompleteStructured(wsContext(t), TaskColdStart, structuredReq(), jsonObjectValidator)
	if err != nil || resp.Text != `{"fixed":true}` {
		t.Fatalf("retry: %v %q", err, resp.Text)
	}
	calls := cheap.Calls()
	if len(calls) != 2 {
		t.Fatalf("retry cost %d calls, want 2", len(calls))
	}
	// The correction turn carries WHY the first answer failed.
	if !strings.Contains(string(calls[1].Payload), "not a JSON object") ||
		!strings.Contains(string(calls[1].Payload), "garbage") {
		t.Fatalf("retry payload lacks the validator feedback: %s", calls[1].Payload)
	}
}

func TestStructuredEscalatesOneTierOnSecondFailure(t *testing.T) {
	cheap := NewFakeClient().Script("bad one", "bad two")
	premium := NewFakeClient().Script(`{"rescued":true}`)
	meter := &memMeter{}
	r := testRouter(map[Tier]model.Client{TierCheapCloud: cheap, TierPremium: premium},
		meter, DefaultMonthlyTokens, ProfileEUHosted)

	resp, info, err := r.CompleteStructured(wsContext(t), TaskColdStart, structuredReq(), jsonObjectValidator)
	if err != nil || resp.Text != `{"rescued":true}` {
		t.Fatalf("escalation: %v %q", err, resp.Text)
	}
	if info.Tier != TierPremium {
		t.Fatalf("third attempt served from %s, want the escalated tier", info.Tier)
	}
	// All three attempts hit the meter — retries are budgeted spend.
	if len(meter.records) != 3 {
		t.Fatalf("metered %d calls, want 3", len(meter.records))
	}
}

func TestStructuredExhaustionIsAnHonestError(t *testing.T) {
	cheap := NewFakeClient().Script("bad", "bad", "bad")
	r := testRouter(map[Tier]model.Client{TierCheapCloud: cheap}, &memMeter{}, DefaultMonthlyTokens, ProfileEUHosted)

	_, _, err := r.CompleteStructured(wsContext(t), TaskColdStart, structuredReq(), jsonObjectValidator)
	if err == nil || !strings.Contains(err.Error(), "failed validation after retry and escalation") {
		t.Fatalf("exhaustion → %v, want the honest degraded error", err)
	}
}
