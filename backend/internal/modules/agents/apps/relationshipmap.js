// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The relationship map view: who_knows's colleagues, warmest first, with the
// interaction count the warmth rests on.
//
// WHY THE BAND AND THE COUNT TOGETHER. A strength score alone is the mystery
// number again, and the seam is careful about a specific case this view has to
// keep honest: strength is ABSENT when the band is "none", because never having
// spoken is not a score of zero. Rendering a missing strength as 0 would tell a
// rep a relationship decayed when none ever existed, so an absent score renders
// as absent.
//
// It renders and nothing else. Introducing someone is a human act with its own
// route; a button here would be this view inventing authority it was not given.

(function () {
  'use strict';

  var el = window.mcpApp.el;

  // The bands the seam reports. A band outside this set still renders — with no
  // colour rather than with the wrong one — because the vocabulary belongs to
  // the seam and a view that refused an unknown value would go blank the first
  // time one was added.
  var BANDS = { high: true, medium: true, low: true, none: true };

  // Object.hasOwn, not a plain lookup: a bucket of "constructor" or "toString"
  // finds a truthy value on the prototype chain and would be rendered as a class
  // this stylesheet does not have.
  function bandClass(bucket) {
    return Object.hasOwn(BANDS, bucket) ? 'band-' + bucket : 'state';
  }

  function strengthText(colleague) {
    // Absent, not zero. See the note at the top of this file.
    if (typeof colleague.strength !== 'number') {
      return colleague.strength_bucket || 'unknown';
    }
    return (colleague.strength_bucket || 'unknown') + ' · ' + colleague.strength;
  }

  function colleagueRow(colleague, position) {
    var row = el('div', 'row');
    var head = el('div', 'row-head');
    head.appendChild(el('span', 'rank', '#' + position));
    head.appendChild(el('span', 'name', colleague.display_name || colleague.user_id));
    head.appendChild(el('span', bandClass(colleague.strength_bucket), strengthText(colleague)));
    row.appendChild(head);
    row.appendChild(
      el('div', 'factors', window.mcpApp.count(colleague.interactions_90d) + ' interactions in 90 days')
    );
    return row;
  }

  // The envelope's code for "this read stopped at its bound". A bounded ranking
  // is not the whole network, and the tool's contract is explicit that a model —
  // or a view — told nothing will report it as one.
  var SWEEP_TRUNCATED = 'sweep_truncated';

  function render(data, warnings) {
    var root = document.getElementById('root');
    root.replaceChildren();
    if (!data) {
      root.appendChild(el('div', 'empty', 'The host sent no structured result for this contact.'));
      return;
    }
    var colleagues = window.mcpApp.list(data.colleagues);
    root.appendChild(el('h1', null, 'Who knows this contact'));
    // "warmest first" is only true of a COMPLETE ranking. When the read stopped
    // at its bound, these are the warmest found, and saying otherwise is the
    // claim the tool refuses to make.
    var bounded = window.mcpApp.warned(warnings, SWEEP_TRUNCATED);
    root.appendChild(el('p', 'meta', bounded
      ? colleagues.length + ' colleague(s) found — more know this contact than are listed, so this is not the whole network'
      : colleagues.length + ' colleague(s), warmest first · ' + (data.person_id || '')));
    if (colleagues.length === 0) {
      root.appendChild(el('div', 'empty', 'Nobody here has spoken to this contact. That is the answer, not a gap.'));
      return;
    }
    var rows = el('div', 'rows');
    // A non-object element is SKIPPED rather than thrown on. One malformed entry
    // would otherwise abort the loop and leave a heading with no rows — a view
    // that looks like an empty answer while the payload had colleagues in it.
    var position = 0;
    colleagues.forEach(function (colleague) {
      if (!colleague || typeof colleague !== 'object') return;
      position += 1;
      rows.appendChild(colleagueRow(colleague, position));
    });
    root.appendChild(rows);
  }

  window.mcpApp.onResult(render);
})();
