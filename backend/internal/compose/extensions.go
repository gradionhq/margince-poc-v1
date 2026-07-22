// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/ports/jurisdiction"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// RegisterExtensions reconciles the composed extension set (the
// generated composition module's Extensions()) into the core registries.
// Every process role calls it exactly once at boot, before any surface
// serves. Declarations are inert values, so validation and application
// are separate phases: an error anywhere — including a
// duplicate-capability panic from a core registry — aborts the boot, and
// no capability applies unless the whole set validated, so a partially
// registered extension never serves. This is also where the manifest
// emission (ADR-0069 §5) and the approval filtering (§7) slot in: both
// operate on the declared set before anything is applied.
func RegisterExtensions(exts []extension.Extension) error {
	seen := make(map[extension.Name]bool, len(exts))
	packCodes := make(map[jurisdiction.Code]extension.Name, len(exts))
	for _, e := range exts {
		if err := e.Name.Validate(); err != nil {
			return fmt.Errorf("compose: %w", err)
		}
		if seen[e.Name] {
			return fmt.Errorf("compose: extension %q composed twice — the enabled set under extensions/ carries one directory per unit", e.Name)
		}
		seen[e.Name] = true
		if err := e.Version.Validate(); err != nil {
			return fmt.Errorf("compose: extension %q: %w", e.Name, err)
		}
		// Every capability is preflighted here, against the declared set
		// AND the live registry, so the apply loop below cannot fail
		// halfway: a mid-apply abort would leave an earlier unit's packs
		// registered while the boot reports failure.
		for _, p := range e.Jurisdictions {
			code := p.Code()
			if err := code.Validate(); err != nil {
				return fmt.Errorf("compose: extension %q: %w", e.Name, err)
			}
			if owner, dup := packCodes[code]; dup {
				return fmt.Errorf("compose: extensions %q and %q both declare jurisdiction %q", owner, e.Name, code)
			}
			if _, taken := jurisdiction.For(code); taken {
				return fmt.Errorf("compose: extension %q declares jurisdiction %q, which a core pack already registers", e.Name, code)
			}
			packCodes[code] = e.Name
		}
	}
	for _, e := range exts {
		for _, p := range e.Jurisdictions {
			jurisdiction.Register(p)
		}
	}
	return nil
}
