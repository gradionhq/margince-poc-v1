// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deployconfig

import (
	"fmt"
	"os"
	"strings"

	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/shared/runtimeenv"
)

// LicenseTokenEnvVar overrides license.token_file when it is set. It is the
// same variable name the bundled validation module reads its own token from, so
// a container that already exports the license needs no configuration file at
// all.
const LicenseTokenEnvVar = "MARGINCE_LICENSE"

// License points at the installation's entitlement token. The token is a file
// reference, never an inline value: it is a credential, and this file is
// routinely read, copied and pasted into a support thread.
//
// An installation with no license section runs unlicensed — every development
// and CI process in this repository does.
type License struct {
	// TokenRef is the reference form: ${file:...} or ${env:...}. Named for the
	// reference rather than the value, because Token() below is what hands a
	// caller the value and the two must not read alike.
	TokenRef Secret `yaml:"token"`
	// TokenFile is the original spelling, still honoured so an existing
	// deployment boots unchanged. Prefer `token`.
	TokenFile string `yaml:"token_file"`
}

// TokenLimit bounds a token file. A license is a JWT — a few hundred bytes, a
// few thousand at the outside — and everything downstream copies it whole: into
// a process environment, into a WebAssembly module's linear memory, and into
// whatever the module quotes back on refusal. A pointed-at-the-wrong-file
// mistake (a log, an image) must fail as one rather than being carried.
const TokenLimit = 64 << 10

// Token resolves the license token: the environment variable when set,
// otherwise the file reference, otherwise empty for an unlicensed
// installation.
//
// A configured file that cannot be read, that holds nothing, or that is too
// large to be a license is an ERROR rather than an unlicensed installation.
// Those two are the same posture to every later caller, and an operator who
// typed the path wrong would otherwise get a workspace that silently believes it
// has no entitlement — which is exactly the failure the file's strict decoding
// (an unknown key is a boot error) exists to prevent everywhere else.
func (l License) Token(lookup config.Lookup) (string, error) {
	if token := strings.TrimSpace(lookup(LicenseTokenEnvVar)); token != "" {
		return token, nil
	}
	// The reference form, where a deployment used it. It outranks token_file
	// for the same reason the environment outranks both: it is the newer,
	// deliberate spelling, and an operator who wrote one did not also mean the
	// other.
	if l.TokenRef.Configured() {
		ref := l.TokenRef.withField("license.token")
		token, err := ref.Resolve(lookup)
		if err != nil {
			return "", err
		}
		// A named source that yielded nothing is a mistake, not an unlicensed
		// installation — the same rule token_file has enforced below all along.
		// Without this the newer spelling would be the WEAKER one: an operator
		// whose mounted secret failed to project would be told their
		// installation has no license rather than that the file they named is
		// empty, and in production those two produce the same refusal with the
		// wrong remedy attached.
		if token == "" {
			return "", ref.Missing()
		}
		return token, nil
	}
	if l.TokenFile == "" {
		return "", nil
	}
	info, err := os.Stat(l.TokenFile)
	if err != nil {
		return "", fmt.Errorf("deployconfig: reading license.token_file: %w", err)
	}
	if info.Size() > TokenLimit {
		return "", fmt.Errorf("deployconfig: license.token_file %s is %d bytes; a license token is not (limit %d) — "+
			"check the path points at the token and not at something else", l.TokenFile, info.Size(), TokenLimit)
	}
	raw, err := os.ReadFile(l.TokenFile) // #nosec G304 -- the operator's own token path; reading it is the function's purpose
	if err != nil {
		return "", fmt.Errorf("deployconfig: reading license.token_file: %w", err)
	}
	// A token is a JWT and carries no internal whitespace, so trimming both ends
	// tolerates however the operator's editor or secret store terminated the
	// file.
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("deployconfig: license.token_file %s is empty — remove the setting to run unlicensed, "+
			"or write the license token into the file", l.TokenFile)
	}
	return token, nil
}

// TokenSource binds a lookup to Token, giving the license watcher something it
// can call repeatedly. The watcher re-reads on every check so a license the
// operator renews in place takes effect without a restart — which means the
// environment has to travel WITH the source rather than being sampled once at
// wiring time, or a token rotated in the environment would never be seen.
func (l License) TokenSource(lookup config.Lookup) func() (string, error) {
	return func() (string, error) { return l.Token(lookup) }
}

// TokenOriginNone is what TokenOrigin answers when the deployment names no
// token at all. Named because a caller has to compare against it — the answer
// "nowhere in this file" is the one a caller may need to override with a source
// this package has never heard of — and two spellings of it would drift.
const TokenOriginNone = "none"

// TokenOrigin names where Token would take a token from, for the boot log.
//
// Which of the two won is worth saying out loud: the environment outranks the
// file, so an installation can be pointed at a different license — or, with the
// file absent, at none — by whoever can set a variable in the deploy pipeline
// without touching the deployment file the operator reviews.
func (l License) TokenOrigin(lookup config.Lookup) string {
	if strings.TrimSpace(lookup(LicenseTokenEnvVar)) != "" {
		return LicenseTokenEnvVar
	}
	if l.TokenRef.Configured() {
		return "license.token"
	}
	if l.TokenFile != "" {
		return "license.token_file"
	}
	return TokenOriginNone
}

// ConfigItems declares the two variables that belong to the DEPLOYMENT rather
// than to any one process role: the posture, and the licence override.
//
// Here rather than in each cmd/<role>, because both roles read both and a
// per-role copy is a doc string that drifts. Here rather than in
// shared/runtimeenv, because that package is Tier-0 and stdlib-only — it cannot
// import platform/config, so the name travels up to the package that already
// owns the licence half.
func ConfigItems() []config.Item {
	both := []string{config.RoleAPI, config.RoleWorker}
	return []config.Item{
		{
			Name: runtimeenv.EnvVar, Kind: config.KindString, Default: "production", Roles: both,
			Doc: "deployment posture: dev|test honour the non-production licence authorities and may run unlicensed; anything else, staging included, is production",
		},
		{
			Name: LicenseTokenEnvVar, Kind: config.KindString, Secret: true, Roles: both,
			Doc: "licence token, overriding license.token_file when non-empty",
		},
	}
}
