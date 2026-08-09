// Click the Run button. Do not count it — click it.
//
//   node scripts/check-run-button.js http://127.0.0.1:7700
//
// check-live-page.js asserted "80 Run buttons rendered" while the buttons did
// nothing when pressed. Rendering a button and wiring a button are different
// facts, and only one of them was being checked.
//
// This runs the served page's own script against a fake DOM, finds the Run
// button for a real command, invokes its click handler, and asserts that the
// handler actually called /api/run and that the output reached the page.

const BASE = process.argv[2] || "http://127.0.0.1:7700";

// Run the whole check twice: once with a streaming body, as a browser gives,
// and once without, which is what happened when getReader() was unavailable
// and the page silently produced nothing.
const STREAMING = process.argv[3] !== "--no-stream";

let failures = 0;
const ok = (m) => console.log("  ok    " + m);
const fail = (m, d) => { console.log("  FAIL  " + m + (d ? "\n     " + d : "")); failures++; };

class Node {
  constructor(tag) {
    this.tagName = (tag || "").toUpperCase();
    this.children = []; this.dataset = {}; this.attributes = {};
    // A real element style has setProperty; a bare object does not, and
    // the page uses it to set a custom property. Without this the check
    // failed inside its own fake DOM and blamed the page.
    this.style = { setProperty(k, v) { this[k] = v; }, removeProperty(k) { delete this[k]; } };
    this.listeners = {}; this._text = ""; this.value = ""; this.disabled = false;
    this.open = false; this.colSpan = 1; this.title = "";
    this.classList = {
      _s: new Set(),
      add: (c) => this.classList._s.add(c),
      remove: (c) => this.classList._s.delete(c),
      toggle: (c, on) => (on ? this.classList._s.add(c) : this.classList._s.delete(c)),
      contains: (c) => this.classList._s.has(c),
    };
  }
  get className() { return [...this.classList._s].join(" "); }
  set className(v) { this.classList._s = new Set(String(v).split(/\s+/).filter(Boolean)); }
  get textContent() { return this._text + this.children.map((c) => c.textContent || "").join(""); }
  set textContent(v) { this._text = String(v); this.children = []; }
  append(...n) { for (const x of n) this.children.push(x); }
  prepend(...n) { this.children.unshift(...n); }
  get firstChild() { return this.children[0] || null; }
  removeChild(n) { this.children = this.children.filter((c) => c !== n); }
  setAttribute(k, v) { this.attributes[k] = String(v); }
  getAttribute(k) { return this.attributes[k]; }
  addEventListener(k, h) { (this.listeners[k] ||= []).push(h); }
  querySelector() { return null; }
  querySelectorAll() { return []; }
  // Fire a handler the way a browser would.
  click() {
    for (const handler of this.listeners.click || []) handler({});
  }
}

const ids = {};
const byId = (id) => (ids[id] ||= new Node("div"));

global.document = {
  createElement: (t) => new Node(t),
  getElementById: byId,
  querySelector: () => null,
  querySelectorAll: () => [],
  addEventListener: () => {},
  createTextNode: (t) => { const n = new Node("#text"); n._text = String(t); return n; },
  body: new Node("body"),
};
global.window = { CSS: null, location: {} };
global.confirm = () => true;

function findRunButtons(node, found = []) {
  if (node.tagName === "BUTTON" && /^run$/i.test((node._text || "").trim())) found.push(node);
  for (const child of node.children) findRunButtons(child, found);
  return found;
}

(async () => {
  const realFetch = global.fetch;

  let html, catalogue;
  try {
    const page = await realFetch(BASE + "/");
    html = await page.text();
    const api = await realFetch(BASE + "/api/catalogue");
    catalogue = JSON.parse(await api.text());
  } catch (error) {
    fail("could not reach " + BASE, String(error));
    process.exit(1);
  }
  ok("fetched the served page and catalogue");

  // Record what the page asks for, and answer the way the server does.
  const calls = [];
  global.fetch = async (url, options) => {
    calls.push({ url, options });
    if (String(url).includes("/api/catalogue")) {
      return { ok: true, status: 200, json: async () => catalogue, text: async () => "" };
    }
    if (String(url).includes("/api/run")) {
      const payload = "$ opencenter cluster list\n\nno clusters\n\n[exit 0 · 42ms]\n";
      // The server streams, so the stub streams. A stub that only offers
      // text() tests a path the browser never takes.
      const chunks = STREAMING
        ? [{ done: false, value: Buffer.from(payload) }, { done: true }]
        : null;
      let at = 0;
      return {
        ok: true, status: 200,
        // body is null when STREAMING is off, which is the case the page has
        // to survive rather than swallow.
        body: chunks ? { getReader: () => ({ read: async () => chunks[at++] }) } : null,
        text: async () => payload,
        json: async () => ({}),
      };
    }
    return { ok: true, status: 200, json: async () => ({}), text: async () => "" };
  };

  const script = html.split("<script>")[1].split("</script>")[0];
  try {
    eval(script);
  } catch (error) {
    fail("the served page threw while loading", String(error));
    process.exit(1);
  }
  for (let i = 0; i < 60; i++) await new Promise((r) => setImmediate(r));

  const buttons = findRunButtons(byId("commands"));
  if (buttons.length === 0) {
    fail("no Run buttons were rendered at all");
    process.exit(1);
  }
  ok(buttons.length + " Run buttons rendered");

  // The one that matters: is a handler attached?
  const wired = buttons.filter((b) => (b.listeners.click || []).length > 0);
  if (wired.length === buttons.length) {
    ok("every Run button has a click handler");
  } else {
    fail((buttons.length - wired.length) + " Run buttons have NO click handler",
      "they render, and pressing them does nothing");
    process.exit(1);
  }

  // Are they pressable? A stuck app.running disables all of them for ever.
  const enabled = buttons.filter((b) => !b.disabled);
  if (enabled.length === 0) {
    fail("every Run button is disabled",
      "nothing can be pressed — app.running is probably stuck true");
    process.exit(1);
  }
  ok(enabled.length + " of " + buttons.length + " are enabled (the rest need the mutation gate)");

  // Press one and see whether it reaches the server.
  const before = calls.length;
  enabled[0].click();
  for (let i = 0; i < 60; i++) await new Promise((r) => setImmediate(r));

  const runCalls = calls.slice(before).filter((c) => String(c.url).includes("/api/run"));
  if (runCalls.length === 0) {
    fail("pressing Run called nothing",
      "the handler is attached but never reaches /api/run");
    process.exit(1);
  }
  ok("pressing Run posts to /api/run");

  const body = runCalls[0].options && runCalls[0].options.body;
  if (!body) {
    fail("the request has no body", "the server cannot know which command to run");
  } else {
    let parsed;
    try { parsed = JSON.parse(body); } catch (error) { parsed = null; }
    if (!parsed) fail("the request body is not JSON", String(body).slice(0, 120));
    else if (!parsed.command || !parsed.args) {
      fail("the request names no command", JSON.stringify(parsed));
    } else {
      ok('it sends command "' + parsed.command + '" with args "' + parsed.args + '"');
    }
  }

  // And the reply has to land somewhere a person can see.
  const shown = byId("commands").textContent;
  if (shown.includes("exit 0")) ok("the output is rendered back into the page");
  else fail("the output never reached the page", "the run happened and showed nothing");

  console.log();
  if (failures === 0) console.log("  the Run button works");
  else { console.log("  " + failures + " problem(s)"); process.exit(1); }
})();
