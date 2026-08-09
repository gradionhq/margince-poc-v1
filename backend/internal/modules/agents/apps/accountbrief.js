// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The account brief view: read_brief's queue, with the factor decomposition
// each item ranked on.
//
// WHY THE FACTORS ARE THE POINT. The brief's own contract forbids the mystery
// number — an item that says only "this ranked first" restates the queue, while
// one that says "first on momentum and warmth" has told the person something.
// The score alone would fit in the chat text this view renders instead of; the
// five factors beside it are what a table buys.
//
// It renders and nothing else: no control, no action, no call back into the
// surface. Acting on a brief item is a human-only route by contract, so a button
// here would be a door the contract does not have.

(function () {
  'use strict';

  var el = window.mcpApp.el;

  var FACTORS = [
    ['winnability', 'win'],
    ['revenue', 'rev'],
    ['timing', 'time'],
    ['momentum', 'mom'],
    ['warmth', 'warm'],
  ];

  function factorRow(factors) {
    var wrap = el('div', 'factors');
    var values = factors || {};
    FACTORS.forEach(function (pair) {
      wrap.appendChild(el('span', 'factor', pair[1] + ' ' + window.mcpApp.percent(values[pair[0]])));
    });
    return wrap;
  }

  // The deal is named by ID because that is what the tool answers. A brief item
  // carries no deal name, and inventing a lookup for one would be this view
  // introducing a data path — which is exactly what an App must not do.
  function itemRow(item) {
    var row = el('div', 'row');
    var head = el('div', 'row-head');
    head.appendChild(el('span', 'rank', '#' + window.mcpApp.count(item.rank)));
    head.appendChild(el('span', 'name', item.deal_id));
    head.appendChild(el('span', 'score', window.mcpApp.percent(item.composite)));
    row.appendChild(head);
    row.appendChild(factorRow(item.factors));
    if (item.state) {
      row.appendChild(el('div', 'state', 'state: ' + item.state));
    }
    return row;
  }

  function render(data) {
    var root = document.getElementById('root');
    // Replacing children rather than clearing markup: a view may be sent a
    // second result, and the first one's nodes have to go without any string
    // ever being parsed as markup.
    root.replaceChildren();
    if (!data) {
      root.appendChild(el('div', 'empty', 'The host sent no structured result for this brief.'));
      return;
    }
    var items = window.mcpApp.list(data.items);
    root.appendChild(el('h1', null, 'Morning brief'));
    // candidate_count may exceed the queue, and the difference is what the
    // ranking left out. Reporting both is the brief's own honesty rule.
    root.appendChild(
      el(
        'p',
        'meta',
        items.length +
          ' of ' +
          window.mcpApp.count(data.candidate_count) +
          ' candidates · as of ' +
          (data.as_of || 'unknown')
      )
    );
    if (items.length === 0) {
      root.appendChild(el('div', 'empty', 'Nothing is queued. An empty brief is an answer, not a failure.'));
      return;
    }
    var rows = el('div', 'rows');
    items.forEach(function (item) {
      rows.appendChild(itemRow(item));
    });
    root.appendChild(rows);
  }

  window.mcpApp.onResult(render);
})();
