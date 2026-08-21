// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Blocks, ChevronRight } from "lucide-react";
import {
  EXTENSION_SCREEN,
  type UnitSecretScope,
  unitsForSecretScope,
} from "../app/extensions";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
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
//
// A unit is ONE settings row, not a panel of its own: the page offers it, and
// the only thing to do with the offer is go there. Nothing about a unit is
// configured HERE — its own screen owns that — so a nested panel would draw a
// container around a single verb. The row puts the unit's name in the same
// naming column as every other decision on the page, with the way in where
// every other answer sits.

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
    // Panel, not Card: every other card on the two pages this lands on is one,
    // and two card primitives on one settings tab is how the page grows two
    // header bands that disagree about their own height.
    <Panel title={t(`extUnits.${scope}.title`)}>
      <PanelBody>
        <p className="settings-panel-sub">{t(`extUnits.${scope}.sub`)}</p>
        <SettingList>
          {units.map((unit) => (
            <UnitRow key={unit.name} name={unit.name} />
          ))}
        </SettingList>
      </PanelBody>
    </Panel>
  );
}

/**
 * One unit, as a row whose verb leads to its screen.
 *
 * The row draws the unit's name and the control is the way in, so the link's
 * VISIBLE text can be the verb while its accessible name still carries the
 * unit: a list of links all announced as "Open" names nothing to anyone reading
 * them out of context, which is the trap the whole-row link was avoiding
 * before. The row's naming column wraps, so a long unit name needs no
 * truncation and there is nothing left to reveal on hover.
 */
function UnitRow({ name }: Readonly<{ name: string }>) {
  const t = useT();
  return (
    <SettingRow
      testId={`ext-unit-${name}`}
      label={
        <span className="extunits-name">
          <Blocks aria-hidden />
          {/* Untranslated: it is the INSTALLATION's text, and a catalogue this
              program ships cannot hold a string for a unit it has never seen. */}
          {name}
        </span>
      }
      control={
        <a
          className="extunits-open"
          href={`#/${EXTENSION_SCREEN}/${name}`}
          aria-label={t("extUnits.openNamed", { name })}
        >
          {t("extUnits.open")}
          <ChevronRight aria-hidden />
        </a>
      }
    />
  );
}
