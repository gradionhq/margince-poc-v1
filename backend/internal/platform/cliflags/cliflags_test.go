// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package cliflags

import (
	"flag"
	"strings"
	"testing"
)

// fakeEnv is a getenv the test controls, so no case depends on the process
// environment and none can leak into a sibling.
func fakeEnv(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// The usage text must never carry an environment value. This is the reason the
// package exists: flag renders a non-empty default as `(default "…")`, so a
// credential wired into a default reaches stderr on any parse error.
func TestUsageTextCarriesNoEnvironmentValue(t *testing.T) {
	var env Env
	var dsn, cfg string
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	var usage strings.Builder
	fs.SetOutput(&usage)

	env.String(fs, &dsn, "dsn", "PROBE_DSN", "", "Postgres DSN")
	env.String(fs, &cfg, "config", "PROBE_CONFIG", "probe.yaml", "path to the config file")
	env.Apply(fs, fakeEnv(map[string]string{
		"PROBE_DSN":    "postgres://u:SUPERSECRET@host/db",
		"PROBE_CONFIG": "/etc/probe.yaml",
	}))
	fs.PrintDefaults()

	if strings.Contains(usage.String(), "SUPERSECRET") {
		t.Errorf("the usage text carries the environment's value:\n%s", usage.String())
	}
	// The literal IS echoed on purpose — it tells an operator what happens with no
	// configuration at all, and it is not a secret.
	if !strings.Contains(usage.String(), "probe.yaml") {
		t.Errorf("the literal default is missing, so the usage text stopped being useful:\n%s", usage.String())
	}
}

func TestPrecedenceIsFlagThenEnvironmentThenLiteral(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  string
		want string
	}{
		{"the flag wins over the environment", []string{"--config", "/from/flag"}, "/from/env", "/from/flag"},
		{"the environment wins over the literal", nil, "/from/env", "/from/env"},
		{"the literal is the last resort", nil, "", "probe.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var env Env
			var cfg string
			fs := flag.NewFlagSet("probe", flag.ContinueOnError)
			env.String(fs, &cfg, "config", "PROBE_CONFIG", "probe.yaml", "path")
			if err := fs.Parse(tc.args); err != nil {
				t.Fatalf("parsing %v: %v", tc.args, err)
			}
			env.Apply(fs, fakeEnv(map[string]string{"PROBE_CONFIG": tc.env}))

			if cfg != tc.want {
				t.Errorf("config resolved to %q, want %q", cfg, tc.want)
			}
		})
	}
}

// An explicitly passed empty flag stays empty. Treating it as absent would let a
// wrapper's `--flag "$UNSET_VAR"` silently pick up an ambient value instead.
func TestAnExplicitEmptyFlagIsNotFilledFromTheEnvironment(t *testing.T) {
	var env Env
	var cfg string
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	env.String(fs, &cfg, "config", "PROBE_CONFIG", "probe.yaml", "path")
	if err := fs.Parse([]string{"--config", ""}); err != nil {
		t.Fatalf("parsing: %v", err)
	}
	env.Apply(fs, fakeEnv(map[string]string{"PROBE_CONFIG": "/from/env"}))

	if cfg != "" {
		t.Errorf("an explicit empty --config became %q; the caller's intent was overridden", cfg)
	}
}

// An empty environment value must not erase a literal default: .env.example
// promises a blank line is the same as unset, and env files are sourced wholesale.
func TestAnEmptyEnvironmentValueLeavesTheLiteralAlone(t *testing.T) {
	var env Env
	var cfg string
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	env.String(fs, &cfg, "config", "PROBE_CONFIG", "probe.yaml", "path")
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parsing: %v", err)
	}
	env.Apply(fs, fakeEnv(map[string]string{"PROBE_CONFIG": ""}))

	if cfg != "probe.yaml" {
		t.Errorf("a blank environment value erased the literal default, leaving %q", cfg)
	}
}

func TestEnvKeysNamesEveryBinding(t *testing.T) {
	var env Env
	var a, b string
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	env.String(fs, &a, "one", "PROBE_ONE", "", "")
	env.String(fs, &b, "two", "PROBE_TWO", "", "")

	keys := strings.Join(env.EnvKeys(), ",")
	if keys != "PROBE_ONE,PROBE_TWO" {
		t.Errorf("EnvKeys returned %q; a test that seeds from this would miss a binding", keys)
	}
}

// TestItemsCarryNoEnvironmentValue is the mirror of the usage-text gate above,
// for the other artefact this package now feeds.
//
// Items publishes each flag's DefValue, and a generated template or schema
// renders that. If a registration ever took its default FROM the environment,
// the value would travel there exactly as it once travelled into usage text —
// same leak, new destination.
func TestItemsCarryNoEnvironmentValue(t *testing.T) {
	const sentinel = "a-value-the-environment-supplied"
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	var env Env
	var dsn, level string
	env.String(fs, &dsn, "dsn", "PROBE_DSN", "", "the DSN")
	env.String(fs, &level, "log-level", "PROBE_LOG_LEVEL", "info", "log level")

	// Everything the environment could supply, supplied.
	env.Apply(fs, func(string) string { return sentinel })

	for _, item := range env.Items(fs, "probe", map[string]bool{"PROBE_LOG_LEVEL": true}) {
		if strings.Contains(item.Default, sentinel) {
			t.Errorf("%s carries an environment-supplied default %q into the declared surface", item.Name, item.Default)
		}
	}
}

// A binding nobody classified is withheld, not published: the map miss must
// fail closed, because the recoverable mistake is a redacted value an operator
// asks about and the unrecoverable one is a bearer token in a build log.
func TestAnUnclassifiedBindingIsTreatedAsASecret(t *testing.T) {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	var env Env
	var newKnob string
	env.String(fs, &newKnob, "new-knob", "PROBE_NEW_KNOB", "", "a knob nobody classified")

	items := env.Items(fs, "probe", map[string]bool{})
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if !items[0].Secret {
		t.Error("an unclassified binding was published; the default must be to withhold")
	}
}
