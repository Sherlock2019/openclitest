// Run the page the server actually serves, against the data the server
// actually returns.
//
//   node scripts/check-live-page.js http://127.0.0.1:7707
//
// check-page.js runs the ui.html in the working tree against invented data.
// That passes while the *binary* still has an older page embedded, or while
// the server returns a shape the page cannot read — which is exactly what a
// person saw as a blank screen. This fetches both halves from the running
// server, so the thing under test is the thing being served.

const BASE = process.argv[2] || "http://127.0.0.1:7707";

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
}

const ids = {};
const byId = (id) => (ids[id] ||= new Node("div"));

// Walk the tree rather than trusting textContent: a button's label is the only
// thing textContent shows, and two buttons with the same label are one string.
function countRunButtons(node) {
  let total = node.tagName === "BUTTON" && /^run$/i.test((node._text || "").trim()) ? 1 : 0;
  for (const child of node.children) total += countRunButtons(child);
  return total;
}

function countReleaseFields(node) {
  let total = node.tagName === "INPUT" &&
    node.getAttribute("data-env") === "OPENCLI_VERSION" ? 1 : 0;
  for (const child of node.children) total += countReleaseFields(child);
  return total;
}

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
// TextDecoder is deliberately NOT stubbed. Node's own fetch decodes response
// bodies with it, so replacing it with a stub that returns "" makes every
// response arrive empty — which is exactly what happened, and cost an hour of
// looking at the server for a fault that was in this file.

(async () => {
  // The page's own fetch is replaced further down so the real catalogue can
  // be served to it. Keep a handle on the genuine one first, or every request
  // made after that point silently returns an empty body.
  const realFetch = global.fetch;

  // Fetched as text and parsed here, so a failure says what actually came
  // back rather than only that JSON.parse was unhappy.
  const get = async (path) => {
    const response = await realFetch(BASE + path);
    const body = await response.text();
    return { status: response.status, body };
  };

  let html, catalogue;
  try {
    const page = await get("/");
    if (page.status !== 200 || !page.body) {
      fail("the page did not load", "HTTP " + page.status + ", " + page.body.length + " bytes");
      process.exit(1);
    }
    html = page.body;

    const api = await get("/api/catalogue");
    if (api.status !== 200 || !api.body) {
      fail("the catalogue did not load",
        "HTTP " + api.status + ", " + api.body.length + " bytes");
      process.exit(1);
    }
    try {
      catalogue = JSON.parse(api.body);
    } catch (error) {
      fail("the catalogue is not valid JSON",
        String(error) + " — first 200 bytes: " + api.body.slice(0, 200));
      process.exit(1);
    }
  } catch (error) {
    fail("could not reach the server at " + BASE, String(error));
    process.exit(1);
  }

  ok("fetched the served page (" + html.length + " bytes)");
  ok("fetched the live catalogue (" + catalogue.catalogue.total_commands + " commands)");

  // Serve the real data to the real page.
  global.fetch = async (url) => ({
    ok: true, status: 200,
    json: async () => (url.includes("/api/catalogue") ? catalogue : {}),
    text: async () => "",
  });

  const script = html.split("<script>")[1].split("</script>")[0];
  try {
    eval(script);
  } catch (error) {
    fail("the served page threw while loading", String(error));
    process.exit(1);
  }
  for (let i = 0; i < 60; i++) await new Promise((r) => setImmediate(r));

  const strip = byId("strip").textContent;
  for (const wanted of ["What we test", "Why we test", "Where we test", "How we test"]) {
    if (strip.includes(wanted)) ok('the strip renders "' + wanted + '"');
    else fail('the strip does not render "' + wanted + '"', strip.slice(0, 120));
  }

  const tabs = byId("envtabs").textContent;
  const environments = catalogue.catalogue.environments.map((e) => e.name);
  for (const name of environments) {
    if (tabs.includes(name)) ok('a tab for "' + name + '"');
    else fail('no tab for "' + name + '"', tabs);
  }

  // The command table, with the real catalogue behind it.
  const body = byId("commands").textContent;
  const first = catalogue.catalogue.environments[0];
  const sample = first.commands.slice(0, 6);
  let rendered = 0;
  for (const command of sample) {
    if (body.includes(command.id)) rendered++;
  }
  if (rendered === sample.length) ok("the first " + rendered + " real commands are rendered");
  else fail("only " + rendered + " of " + sample.length + " commands rendered");

  for (const stage of catalogue.catalogue.stage_order) {
    const has = first.commands.some((c) => c.stage === stage);
    if (!has) continue;
    if (body.includes(stage)) ok('stage "' + stage + '" is shown');
    else fail('stage "' + stage + '" is missing');
  }
  const flow = byId("flow").textContent;
  if (flow.includes("results") && flow.indexOf("results") < flow.indexOf("teardown")) {
    ok('the zero-command "results" stage is shown before teardown');
  } else {
    fail('the zero-command "results" stage is missing from the rail', flow);
  }

  const counts = byId("counts").textContent;
  const shown = parseInt(counts, 10);
  if (shown >= 70) ok("the table shows " + shown + " commands");
  else fail("the table shows only " + counts, "expected the whole environment");

  // Two kinds of ready line now, and they have to be counted separately.
  //
  // This used to count occurrences of "opencenter " and call the total "ready
  // -to-run lines". That was accurate while every row invoked the binary. The
  // prerequisites stage runs shell — `command -v git` is not a subcommand of
  // anything — so those rows deliberately carry no prefix, and the old count
  // silently fell short of the row count while still passing its own
  // threshold. A check whose name stops matching what it measures is worse
  // than no check: it reports success about something it is not looking at.
  const cliLines = (body.match(/opencenter /g) || []).length;
  if (cliLines >= 70) ok(cliLines + " opencenter invocations rendered");
  else fail("only " + cliLines + " opencenter invocations", "every CLI command needs one");

  // The prerequisite rows, which nothing verified until now.
  //
  // Asserted against the catalogue rather than against the served HTML: the
  // command table is rendered in the browser from /api/catalogue, so none of
  // these lines are in the document this check downloaded. That the rows
  // reach the page at all is covered by check-run-button.js counting the
  // buttons; what is checked here is that the data behind them is coherent.
  const shellRows = first.commands.filter((c) => c.shell);
  if (!shellRows.length) {
    fail("no shell rows in the catalogue", "the prerequisites stage should supply them");
  } else {
    ok(shellRows.length + " prerequisite checks are shell lines, not CLI arguments");

    // Every one offers both commands, which is the whole point of the stage.
    const withoutInstall = shellRows.filter((c) => !c.install);
    if (!withoutInstall.length) ok("every prerequisite offers a setup command too");
    else fail(withoutInstall.length + " prerequisites have no setup command",
      withoutInstall.slice(0, 3).map((c) => c.id).join(", "));

    // Without a risk level every Run button reads the same, which is the
    // wrong amount of hesitation for "install mise" and "apt install as root"
    // to share. This shipped broken once already.
    const unclassified = shellRows.filter((c) => !c.risk);
    if (!unclassified.length) ok("every prerequisite setup declares what it touches");
    else fail(unclassified.length + " prerequisites have no risk level",
      "their Run buttons cannot say what they will change");

    // A prerequisite check must never mutate: it is the one thing on the page
    // that runs before anybody has agreed to anything.
    const mutating = shellRows.filter((c) => c.mutating);
    if (!mutating.length) ok("no prerequisite check is marked mutating");
    else fail(mutating.length + " prerequisite checks claim to mutate",
      mutating.slice(0, 3).map((c) => c.id).join(", "));
  }
  const releaseFields = countReleaseFields(byId("commands"));
  if (releaseFields === 1) ok("the openCenter install step has a release field");
  else fail("the openCenter release field is missing", String(releaseFields));

  // Commands are asked for per env, per stage AND per task. Stage was already
  // checked above; task was not, and a heading that is never asserted is a
  // heading that quietly disappears.
  const tasks = [...new Set(first.commands.map((c) => c.task).filter(Boolean))];
  let tasksShown = 0;
  const taskIsShown = (task) => {
    if (body.includes(task)) return true;
    // Prerequisite cards deliberately split "1 — Mise" into a number disc
    // and a title, so the two visible pieces are not one text node.
    const parts = task.split("—").map((part) => part.trim()).filter(Boolean);
    return parts.length > 1 && parts.every((part) => body.includes(part));
  };
  for (const task of tasks) if (taskIsShown(task)) tasksShown++;
  if (tasksShown === tasks.length) ok("all " + tasksShown + " task groups are shown");
  else fail(tasksShown + " of " + tasks.length + " task groups shown",
    "missing: " + tasks.filter((task) => !taskIsShown(task)).join(", "));

  // Credentials have to be reachable, and they have to belong to the tab you
  // are on. Showing all twenty everywhere meant the VMware tab offered
  // OpenStack's credentials and none of vSphere's.
  const credentials = byId("creds").textContent;
  const all = catalogue.credentials || [];
  if (all.length === 0) {
    fail("the server offers no credential fields at all");
  } else {
    const selected = catalogue.catalogue.environments[0].id;
    const belongs = (f) => !f.envs || f.envs.length === 0 || f.envs.includes(selected);

    const missing = all.filter(belongs).filter((f) => !credentials.includes(f.env));
    if (missing.length === 0) {
      ok(all.filter(belongs).length + " credential fields rendered for " + selected);
    } else {
      fail(missing.length + " credential fields for " + selected + " are missing",
        missing.slice(0, 5).map((f) => f.env).join(", "));
    }

    const foreign = all.filter((f) => !belongs(f)).filter((f) => credentials.includes(f.env));
    if (foreign.length === 0) {
      ok("no credentials from other environments are shown");
    } else {
      fail(foreign.length + " credentials belong to another environment",
        foreign.slice(0, 5).map((f) => f.env).join(", "));
    }

    // Every environment must offer something usable, or its provider
    // commands can never authenticate.
    for (const env of catalogue.catalogue.environments) {
      const own = all.filter((f) => f.envs && f.envs.includes(env.id));
      if (own.length > 0) ok(env.name + " has " + own.length + " credential field(s) of its own");
      else fail(env.name + " has no credentials of its own",
        "its provider commands cannot authenticate");
    }
  }

  // Every command needs a way to run it.
  const runButtons = countRunButtons(byId("commands"));
  if (runButtons >= 70) ok(runButtons + " Run buttons rendered");
  else fail("only " + runButtons + " Run buttons", "every command needs one");

  // The cluster the deploy and day-two commands need must be buildable from
  // the page, not only from a terminal.
  const cluster = await get("/api/kind");
  if (cluster.status === 200) {
    let state;
    try { state = JSON.parse(cluster.body); } catch (error) { state = null; }
    if (!state) {
      fail("/api/kind did not return JSON", cluster.body.slice(0, 120));
    } else if (!state.cluster) {
      fail("/api/kind names no cluster", cluster.body.slice(0, 120));
    } else if (!state.available) {
      ok("/api/kind answers (kind is not installed here, which it reports)");
    } else if (state.running) {
      ok("the cluster " + state.cluster + " is up with " + state.nodes +
        " node(s)" + (state.nodes === 1 ? "" : " — one was expected"));
      if (state.nodes !== 1) {
        fail("the cluster has " + state.nodes + " nodes",
          "one node is deliberate: three fail on this host, see docs/kind-node-count.md");
      }
    } else {
      ok("/api/kind answers, no cluster built yet");
    }
  } else {
    fail("/api/kind did not answer", "HTTP " + cluster.status);
  }

  console.log();
  if (failures === 0) console.log("  the served page renders the live catalogue");
  else { console.log("  " + failures + " problem(s)"); process.exit(1); }
})();
