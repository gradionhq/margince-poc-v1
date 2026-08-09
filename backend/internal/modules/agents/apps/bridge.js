// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The App-side half of the MCP Apps postMessage transport (SEP-1865,
// 2026-01-26): a view is an MCP client that reaches its host through
// window.parent rather than through a socket.
//
// It is loaded into EVERY view, assembled inline, so the handshake exists once.
// A per-view copy would be two implementations of one protocol, and the failure
// they would eventually differ on is the one where a view renders nothing
// because it never announced itself.
//
// WHAT THIS FILE REFUSES TO DO, and why each refusal is load-bearing:
//
//   It never builds DOM from a string. Everything a view displays arrives in
//   `structuredContent`, which is customer data — a person's name, a note
//   someone pasted, the subject line of an ingested email. That is untrusted
//   text by this system's own reckoning, and the view runs inside a sandbox
//   whose whole job is to contain it. Assigning that text as MARKUP would hand
//   it the one privilege the sandbox cannot take back: execution inside the
//   view's own origin. So text reaches the page only through textContent, and
//   structure only through createElement.
//
//   The markup-assignment properties are not named here, deliberately: the sweep
//   in appsfitness_test.go reads the assembled document, and prose that spelled
//   one would trip a check on the very thing it was explaining.
//
//   It holds no credential. A view is given a tool's ANSWER, never the means to
//   ask again. There is no token here to leak, which is a stronger property
//   than a token handled carefully.
//
//   It calls no tool. The protocol permits it; nothing here needs it, and it is
//   the widest part of the extension's surface. A view that stays a renderer
//   cannot become a second door onto a record.

(function () {
  'use strict';

  var PROTOCOL_VERSION = '2026-01-26';
  var nextID = 1;
  var resultHandler = null;
  // The id our own ui/initialize was sent under. Declared here rather than
  // assigned at the end of the file: handle() reads it, and a reader should not
  // have to reason about hoisting to know it is set before a message can arrive.
  var initializeID = null;
  // Whether the handshake has completed. A view is only supposed to be given a
  // result after it has announced itself, and the two states below are what let
  // this refuse anything out of order — see handle().
  var initialized = false;
  // The host's origin, LEARNED rather than configured.
  //
  // A view is loaded into an opaque sandbox origin and cannot know its host's
  // ahead of time — reading window.parent.origin throws cross-origin, and the
  // specification therefore prescribes '*' for the opening message. But the
  // response to it arrives with the host's origin attached, so from that point
  // on there IS something to pin: every later message is sent to that origin
  // and accepted only from it.
  //
  // 'null' is kept as a sentinel rather than pinned. An opaque origin reports
  // itself as the STRING "null", which is not a usable postMessage target, so a
  // host in one keeps the wildcard — the sender check below is what holds there.
  var hostOrigin = null;

  // Every inbound message is checked on TWO things: it came from the frame that
  // embedded us, and — once the host's origin is known — from that origin.
  //
  // The sender check is the stronger of the two and it holds from the first
  // message: a sandboxed view can still be messaged by anything holding a handle
  // to its window, and a view that rendered whatever arrived would let a second
  // sender choose what the human is shown. The origin check adds what the sender
  // check cannot see, which is a host that navigated the parent frame somewhere
  // else between the handshake and the result.
  function fromHost(event) {
    if (event.source !== window.parent) return false;
    if (hostOrigin === null || hostOrigin === 'null') return true;
    return event.origin === hostOrigin;
  }

  function send(message) {
    // Pinned to the host's origin once it is known, and '*' only for the opening
    // message, which is sent before there is anything to learn it from.
    //
    // Nothing sensitive travels outward on either path: what this view sends is
    // a handshake — an initialise request naming the protocol revision, and its
    // confirmation. No record, no credential, no customer text ever leaves here,
    // because a view is given an answer and never the means to ask again.
    var target = hostOrigin === null || hostOrigin === 'null' ? '*' : hostOrigin;
    window.parent.postMessage({ jsonrpc: '2.0', ...message }, target);
  }

  function announce() {
    var id = nextID++;
    send({
      id: id,
      method: 'ui/initialize',
      params: {
        protocolVersion: PROTOCOL_VERSION,
        capabilities: {},
        clientInfo: { name: 'margince-view', version: '1' },
      },
    });
    return id;
  }

  function handle(event) {
    if (!fromHost(event)) return;
    var message = event.data;
    if (message?.jsonrpc !== '2.0') return;
    // The response to our own ui/initialize is the readiness signal: the host
    // is listening, so we confirm and then wait to be given a result.
    //
    // The id is CLEARED as it is consumed, so a repeated response cannot make the
    // view announce itself twice. A host that received two
    // ui/notifications/initialized would be entitled to read the second as a
    // fresh view and re-send everything it had already sent.
    // `'result' in message` rather than a truthy check: `"result": null` is a
    // legal JSON-RPC response, and treating it as "not answered yet" left the
    // view permanently blank — no re-announce, no timeout, every later result
    // dropped, and nothing anywhere saying why.
    if (initializeID !== null && message.id === initializeID && 'result' in message) {
      initializeID = null;
      initialized = true;
      // Learned here and only here, from the one message whose sender is already
      // proven to be the embedding frame.
      hostOrigin = event.origin;
      applyTheme(message.result?.hostContext);
      send({ method: 'ui/notifications/initialized', params: {} });
      return;
    }
    // A result BEFORE the handshake is dropped rather than rendered. The view has
    // not been told the theme or the display mode yet, so rendering then shows
    // the human a panel drawn against defaults the host already corrected — and
    // accepting data outside the sequence is how a view ends up rendering
    // whatever arrives whenever it arrives.
    if (message.method === 'ui/notifications/tool-result' && initialized && resultHandler) {
      // `params` IS the CallToolResult — `structuredContent` sits directly on it,
      // not under a nested member — and that structuredContent is the envelope
      // every tool on this surface seals its answer into: `data` is the tool's
      // own result, `warnings` are the conditions the answer came with.
      //
      // THE WARNINGS ARE PASSED ON, not dropped. A bounded read reports its bound
      // as a warning rather than in its data — `who_knows` stops at a cap and
      // says so — so a renderer given only `data` would present a truncated list
      // as the whole network. That is the one thing the tool's own contract
      // forbids in those words.
      var envelope = message.params?.structuredContent;
      resultHandler(envelope?.data ?? null, envelope?.warnings ?? []);
    }
  }

  // The host tells us which way round it is drawn. Following it is the whole
  // reason a view looks embedded rather than pasted in.
  function applyTheme(hostContext) {
    if (hostContext?.theme) {
      document.documentElement.dataset.theme = hostContext.theme;
    }
  }

  // el() is the ONLY way anything reaches the page, and it takes text rather
  // than markup. The three helpers beside it exist so a view never has to reach
  // past it to format a number or iterate a list that the host did not send.
  window.mcpApp = {
    // onResult(fn) — fn(data, warnings). warnings is always an array, so a
    // renderer never has to decide whether an absent one means "none" or "not
    // computed".
    onResult: function (fn) {
      resultHandler = fn;
    },
    // warned(warnings, code) reports whether one condition was raised. The codes
    // belong to the envelope, so a view asks by code rather than reading prose
    // it would then have to keep in step with the server's wording.
    warned: function (warnings, code) {
      return (Array.isArray(warnings) ? warnings : []).some(function (w) {
        return w && w.code === code;
      });
    },
    el: function (tag, className, text) {
      var node = document.createElement(tag);
      if (className) node.className = className;
      if (text !== undefined && text !== null) node.textContent = String(text);
      return node;
    },
    // A number a view displays as a proportion. Anything that is not a finite
    // number renders as an em dash rather than as "NaN" or "undefined": a view
    // is looking at data it did not produce, and a missing field is a thing
    // that happens.
    percent: function (value) {
      if (!Number.isFinite(value)) return '—';
      return Math.round(value * 100) + '%';
    },
    count: function (value) {
      if (!Number.isFinite(value)) return '—';
      return String(value);
    },
    // A list a view iterates. A field the host did not send is an empty list,
    // never an exception that leaves the view blank with no explanation.
    list: function (value) {
      return Array.isArray(value) ? value : [];
    },
  };

  window.addEventListener('message', handle);
  initializeID = announce();
})();
