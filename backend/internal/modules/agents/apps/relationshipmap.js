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

  function bandClass(bucket) {
    return BANDS[bucket] ? 'band-' + bucket : 'state';
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

  function render(data) {
    var root = document.getElementById('root');
    root.replaceChildren();
    if (!data) {
      root.appendChild(el('div', 'empty', 'The host sent no structured result for this contact.'));
      return;
    }
    var colleagues = window.mcpApp.list(data.colleagues);
    root.appendChild(el('h1', null, 'Who knows this contact'));
    root.appendChild(el('p', 'meta', colleagues.length + ' colleague(s), warmest first · ' + (data.person_id || '')));
    if (colleagues.length === 0) {
      root.appendChild(el('div', 'empty', 'Nobody here has spoken to this contact. That is the answer, not a gap.'));
      return;
    }
    var rows = el('div', 'rows');
    colleagues.forEach(function (colleague, index) {
      rows.appendChild(colleagueRow(colleague, index + 1));
    });
    root.appendChild(rows);
  }

  window.mcpApp.onResult(render);
})();
