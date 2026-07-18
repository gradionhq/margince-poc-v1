// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The free-mail blocklist (CAP-PARAM-5): consumer mail domains that must
// never derive an organization — alice@gmail.com is a person, "Gmail" is
// not her company. The gate suppresses ORG derivation only; the person is
// still created. The baseline is pinned here (founder decision: config
// file, no admin UI); a deployment appends via margince.yaml
// capture.freemail_extra.

import "strings"

// freemailBaseline is the pinned consumer-domain set. Additions land
// through the spec (CAP-PARAM-5), not ad-hoc edits.
var freemailBaseline = map[string]struct{}{
	"gmail.com":       {},
	"googlemail.com":  {},
	"yahoo.com":       {},
	"yahoo.de":        {},
	"yahoo.co.uk":     {},
	"yahoo.fr":        {},
	"ymail.com":       {},
	"hotmail.com":     {},
	"hotmail.de":      {},
	"hotmail.co.uk":   {},
	"hotmail.fr":      {},
	"outlook.com":     {},
	"outlook.de":      {},
	"live.com":        {},
	"live.de":         {},
	"msn.com":         {},
	"aol.com":         {},
	"icloud.com":      {},
	"me.com":          {},
	"mac.com":         {},
	"gmx.de":          {},
	"gmx.net":         {},
	"gmx.at":          {},
	"gmx.ch":          {},
	"gmx.com":         {},
	"web.de":          {},
	"t-online.de":     {},
	"freenet.de":      {},
	"posteo.de":       {},
	"mailbox.org":     {},
	"proton.me":       {},
	"protonmail.com":  {},
	"protonmail.ch":   {},
	"tutanota.com":    {},
	"tuta.io":         {},
	"fastmail.com":    {},
	"zoho.com":        {},
	"mail.com":        {},
	"mail.ru":         {},
	"yandex.com":      {},
	"yandex.ru":       {},
	"seznam.cz":       {},
	"orange.fr":       {},
	"wanadoo.fr":      {},
	"free.fr":         {},
	"libero.it":       {},
	"virgilio.it":     {},
	"telenet.be":      {},
	"ziggo.nl":        {},
	"bluewin.ch":      {},
	"comcast.net":     {},
	"verizon.net":     {},
	"att.net":         {},
	"sbcglobal.net":   {},
	"btinternet.com":  {},
	"sky.com":         {},
	"rogers.com":      {},
	"shaw.ca":         {},
	"bigpond.com":     {},
	"optusnet.com.au": {},
	"qq.com":          {},
	"163.com":         {},
	"126.com":         {},
	"naver.com":       {},
	"daum.net":        {},
	"rediffmail.com":  {},
	"hushmail.com":    {},
	"duck.com":        {},
	"pm.me":           {},
	"hey.com":         {},
}

// FreemailList answers "is this a consumer mail domain?" — case-insensitive,
// against the pinned baseline plus any deployment-configured extras.
type FreemailList struct {
	extra map[string]struct{}
}

// NewFreemailList builds the matcher; extra is the deployment's
// margince.yaml capture.freemail_extra additions (may be nil).
func NewFreemailList(extra []string) *FreemailList {
	l := &FreemailList{extra: make(map[string]struct{}, len(extra))}
	for _, d := range extra {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" {
			l.extra[d] = struct{}{}
		}
	}
	return l
}

// IsFreemail reports whether domain is a consumer mail domain (org
// derivation must be suppressed for it).
func (l *FreemailList) IsFreemail(domain string) bool {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return false
	}
	if _, hit := freemailBaseline[domain]; hit {
		return true
	}
	_, hit := l.extra[domain]
	return hit
}
