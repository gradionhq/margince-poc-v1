// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Blocks, ChevronRight } from "lucide-react";
import {
  EXTENSION_SCREEN,
  type UnitSecretScope,
  unitsForSecretScope,
} from "../app/extensions";
import { Card } from "../design-system/atoms";
import { useTruncationTooltip } from "../design-system/tooltip";
import { useT } from "../i18n";
import "./extension-units.css";

// Where an installation's own units are offered: on the settings page that
// already holds the kind of credential each one is configured with.
//
// A unit used to have a row in the navigation rail, which put an
// installation's surface beside Pipeline and Reports as though the product had
// grown a destination. It has not — enabling a unit adds a thing to CONFIGURE,
// and this is the page that configures things.
//
// WHICH page is the manifest's decision and not this component's. A unit
// declaring a `user` secret holds one member's own credential at a provider,
// so it is offered on their personal Connections page; a `workspace` secret is
// the installation's, so its unit is offered under Integrations. Both pages
// already mean exactly that, which is why the declared scope is enough and no
// unit names a destination. See app/extensions.ts's unitsForSecretScope.
//
// The permission story is the page, and it was already written: Connections is
// per-person and ungated, Integrations is the organization's and opens on the
// grants its cards ask for OR on a workspace-scoped unit being composed at all,
// because this page is the only place such a unit is offered. The rows here add
// no gate of their own — the rail rows they replace had none either, and a
// unit's screen refuses independently on the object it declares.

/**
 * The units whose credential lives in `scope`, or nothing at all.
 *
 * Nothing at all is the vanilla tree, where no unit is composed, and it is also
 * the honest answer for a page whose scope no composed unit declares. A heading
 * over an empty list is a promise the installation did not make — the same
 * reason the rail group this replaces was absent rather than empty.
 */
export function ExtensionUnitsCard({
  scope,
}: Readonly<{ scope: UnitSecretScope }>) {
  const t = useT();
  const units = unitsForSecretScope(scope);
  if (units.length === 0) {
    return null;
  }
  return (
    <Card title={t(`extUnits.${scope}.title`)} sub={t(`extUnits.${scope}.sub`)}>
      <ul className="extunits-list">
        {units.map((unit) => (
          <UnitRow key={unit.name} name={unit.name} />
        ))}
      </ul>
    </Card>
  );
}

/**
 * One unit, as a row that leads to its screen.
 *
 * The unit's NAME is the link, rather than a name beside an "Open" button: a
 * list of identical "Open" links reads as "Open, Open, Open" to a screen
 * reader, and the one word that distinguishes them is the one the link should
 * be named by.
 */
function UnitRow({ name }: Readonly<{ name: string }>) {
  // A unit name is a directory name and can be any length, while the row is one
  // line — so the whole of it is on hover and on focus, and a name that already
  // fits gets no tooltip at all.
  const tip = useTruncationTooltip<HTMLSpanElement>(name);
  return (
    <li>
      <a className="extunits-row" href={`#/${EXTENSION_SCREEN}/${name}`}>
        <Blocks aria-hidden />
        {/* Untranslated: it is the INSTALLATION's text, and a catalogue this
            program ships cannot hold a string for a unit it has never seen. */}
        <span className="extunits-name" ref={tip.ref} {...tip.trigger}>
          {name}
          {tip.tip}
        </span>
        <ChevronRight aria-hidden />
      </a>
    </li>
  );
}
