// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build !integration

package hubspot

import "net/http"

// guardEgress is the production half of a build-tag split, and it is the whole
// of it: the client goes out as its caller built it. The integration half in
// httpclient_integration.go stops that client leaving the machine — see there
// for why the lane needs it and why netguard cannot provide it.
func guardEgress(hc *http.Client) *http.Client { return hc }
