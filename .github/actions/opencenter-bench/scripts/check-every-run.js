// Press every Run button on the page, one at a time, and check each one.
//
//   node scripts/check-every-run.js http://127.0.0.1:PORT
//
// check-run-button.js presses one button and proves the wiring works at
// all. This presses all of them, and adds the question that one could not
// ask: does a run leave the table standing? Rebuilding it on Run is what
// threw the page back to stage 1.
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

  // ------------------------------------------------------------------
  // Press every Run button on the page. Not count them — press them.
  //
  // A button that renders, is wired, and posts the wrong id is a button that
  // runs somebody else's command. That has to be checked per button, not in
  // aggregate, which is why this walks the whole tree and presses each one.
  // ------------------------------------------------------------------

  const walk = (node, out = []) => {
    if (!node) return out;
    if (node.getAttribute && node.getAttribute("data-run-for")) out.push(node);
    for (const child of (node.children || [])) walk(child, out);
    return out;
  };

  const buttons = walk(byId("commands"));
  if (!buttons.length) {
    fail("no Run buttons carry data-run-for",
      "setRunButtons cannot find them, so a run will rebuild the table instead");
    process.exit(1);
  }
  ok(buttons.length + " Run buttons found, each tagged with its command");

  // Every button must name a command the catalogue actually has, and no two
  // may name the same one.
  const cat = catalogue.catalogue || catalogue;
  const env = cat.environments[0];
  const known = new Set(env.commands.map((c) => c.id));
  const seen = new Set();
  let unknown = 0, duplicate = 0;
  for (const button of buttons) {
    const id = button.getAttribute("data-run-for");
    if (!known.has(id)) unknown++;
    if (seen.has(id)) duplicate++;
    seen.add(id);
  }
  if (unknown) fail(unknown + " buttons name a command not in the catalogue");
  else ok("every button names a real command");
  if (duplicate) fail(duplicate + " commands have more than one Run button");
  else ok("no command has two Run buttons");

  // Explicit type, or a button inside a form navigates the page.
  const untyped = buttons.filter((b) => b.type !== "button");
  if (untyped.length) {
    fail(untyped.length + " Run buttons have no type=button",
      "a <button> defaults to submit and would navigate the page");
  } else ok("every Run button is type=button");

  // Now press them. One at a time, waiting for each, because the page only
  // permits one run at a time and a second press while one is in flight is a
  // different test.
  let pressed = 0, silent = 0, wrongID = 0, destroyed = 0;
  const badly = [];

  for (const button of buttons) {
    const id = button.getAttribute("data-run-for");
    if (button.disabled) continue;               // gated rows are meant to be dead

    const before = calls.length;
    const rowsBefore = walk(byId("commands")).length;
    button.click();
    for (let i = 0; i < 40; i++) await new Promise((r) => setImmediate(r));

    const posted = calls.slice(before).filter((c) =>
      String(c.url).includes("/api/run") && c.options && c.options.method === "POST");
    if (!posted.length) { silent++; badly.push(id + " (pressed, posted nothing)"); continue; }

    let body = {};
    try { body = JSON.parse(posted[0].options.body); } catch (error) {}
    if (body.command !== id) {
      wrongID++;
      badly.push(id + " (posted " + body.command + ")");
    }

    // The table must survive a run. If the button count changed, the page
    // rebuilt itself — which is what threw the scroll to the top.
    const rowsAfter = walk(byId("commands")).length;
    if (rowsAfter !== rowsBefore) {
      destroyed++;
      badly.push(id + " (rebuilt the table: " + rowsBefore + " -> " + rowsAfter + ")");
    }
    pressed++;
  }

  ok(pressed + " Run buttons pressed");
  if (silent) fail(silent + " buttons did nothing when pressed", badly.slice(0, 5).join("; "));
  else ok("every pressed button called the server");
  if (wrongID) fail(wrongID + " buttons ran the wrong command", badly.slice(0, 5).join("; "));
  else ok("every button ran its own command");
  if (destroyed) {
    fail(destroyed + " runs rebuilt the command table",
      "that is what throws the page back to stage 1: " + badly.slice(0, 3).join("; "));
  } else ok("no run rebuilt the table — the page stays where it is");

  console.log();
  console.log(failures ? "  " + failures + " problem(s)" : "  every Run button works");
  process.exit(failures ? 1 : 0);
})();
