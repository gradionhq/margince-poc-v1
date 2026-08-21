// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The attachment-per-message cap as a fitness function.
//
// The number appears in five places and NOTHING makes them agree: `maxItems` on
// three request schemas in the contract, prose in the channel directory's
// `max_files` description, the Go bound the send actually enforces
// (activities.maxAttachmentsPerSend), and the carriage each sending connector
// declares (gmail's and telegram's maxSendableFiles). The contract's is the
// authority; the others are checked against it here.
//
// It is worth a gate rather than a comment because the two failure directions are
// both silent and both wrong in a way a reader would not suspect. A Go bound
// LOWER than the contract refuses a request the published contract says is legal,
// at the door, with a 422 naming a limit no client was told about. A Go bound
// HIGHER is worse: nothing in this stack validates a body against its schema, so
// the contract's maxItems is documentation, and the Go number is the only thing
// standing between one request and thousands of per-file transactions.

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// goAttachmentCaps are the Go statements of the cap, each read out of its own
// source rather than restated here: a gate that carried its own copy of the
// number would be one more place to update.
var goAttachmentCaps = map[string]struct {
	file    string
	pattern *regexp.Regexp
}{
	"activities.maxAttachmentsPerSend": {
		file:    "internal/modules/activities/sendattachments.go",
		pattern: regexp.MustCompile(`(?m)^const maxAttachmentsPerSend = (\d+)$`),
	},
	"gmail.maxSendableFiles": {
		file:    "internal/modules/capture/gmail/send.go",
		pattern: regexp.MustCompile(`(?m)^const maxSendableFiles = (\d+)$`),
	},
	// Telegram states its bound inside a grouped const, beside the two other
	// numbers the same upload enforces, so the pattern is anchored on the name
	// rather than on `const`.
	"telegram.maxSendableFiles": {
		file:    "internal/modules/capture/telegram/sendfiles.go",
		pattern: regexp.MustCompile(`(?m)^\tmaxSendableFiles\s+= (\d+)$`),
	},
}

// TestTheSendAttachmentCapMatchesTheContract holds every Go statement of the cap
// to the contract's own maxItems.
func TestTheSendAttachmentCapMatchesTheContract(t *testing.T) {
	want := contractAttachmentCap(t)
	for name, site := range goAttachmentCaps {
		src, err := os.ReadFile(site.file)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		match := site.pattern.FindSubmatch(src)
		if match == nil {
			t.Fatalf("%s: %s no longer declares the cap in the shape this gate reads — "+
				"restate it as a single const, or the gate silently stops checking it", name, site.file)
		}
		got, err := strconv.Atoi(string(match[1]))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want {
			t.Errorf("%s is %d and the contract's attachment_ids maxItems is %d; "+
				"a lower bound refuses a request the contract says is legal, and a higher one is the only "+
				"thing bounding a request nothing else validates", name, got, want)
		}
	}
}

// contractAttachmentCap reads `attachment_ids`' maxItems out of api/crm.yaml and
// insists every request schema that has the field agrees.
//
// Read as text rather than through a YAML tree deliberately: the three schemas
// carry the field at the same indentation with the same following line, so the
// property under test — that they all say the same number — is exactly what a
// scan of the occurrences answers. A parse would need the three schema names
// listed here, and a fourth request growing the field would then go unchecked.
func contractAttachmentCap(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	maxItems := regexp.MustCompile(`^\s*maxItems: (\d+)\s*$`)
	caps := map[int]int{}
	for i, line := range lines {
		if strings.TrimSpace(line) != "attachment_ids:" {
			continue
		}
		// The field's own bound is within the next few lines of its declaration;
		// anything further away is a different property.
		for _, following := range lines[i+1 : min(i+5, len(lines))] {
			if m := maxItems.FindStringSubmatch(following); m != nil {
				value, convErr := strconv.Atoi(m[1])
				if convErr != nil {
					t.Fatal(convErr)
				}
				caps[value]++
				break
			}
		}
	}
	switch {
	case len(caps) == 0:
		t.Fatal("no attachment_ids field in the contract declares a maxItems; the cap is then " +
			"whatever each Go site happens to say, which is the drift this gate exists to catch")
	case len(caps) > 1:
		t.Fatalf("the contract's attachment_ids fields declare %d different maxItems (%v); "+
			"one operation admitting more files than another is a difference no caller can explain", len(caps), caps)
	}
	for value := range caps {
		return value
	}
	return 0
}
