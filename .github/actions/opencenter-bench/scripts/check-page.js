// Run the console's own JavaScript against a fake DOM and a fake server.
//
// A page can be served perfectly and still render nothing. That has happened
// here before: an HTTP smoke test passed while the command list never loaded,
// because the test called the API and the page never got that far. This runs
// the code that actually matters.
//
//   node scripts/check-page.js

const fs = require("fs");
const path = require("path");

const html = fs.readFileSync(path.join(__dirname, "..", "cmd", "testlab", "ui.html"), "utf8");
const script = html.split("<script>")[1].split("</script>")[0];

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
  get textContent() {
    return this._text + this.children.map((c) => c.textContent || "").join("");
  }
  set textContent(v) { this._text = String(v); this.children = []; }
  append(...nodes) { for (const n of nodes) this.children.push(n); }
  prepend(...nodes) { this.children.unshift(...nodes); }
  get firstChild() { return this.children[0] || null; }
  removeChild(n) { this.children = this.children.filter((c) => c !== n); }
  setAttribute(k, v) { this.attributes[k] = String(v); }
  getAttribute(k) { return this.attributes[k]; }
  removeAttribute(k) { delete this.attributes[k]; }
  // Fire a handler the way a browser would, so a check can press a button
  // rather than assert it exists.
  click() { for (const handler of this.listeners.click || []) handler({}); }
  addEventListener(k, h) { (this.listeners[k] ||= []).push(h); }
  querySelector() { return null; }
  querySelectorAll() { return []; }
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
  // The page prepends an error banner to document.body when boot throws.
  // Without a body here that banner threw instead, and the real error was
  // replaced by "cannot read properties of undefined".
  body: new Node("body"),
};
// documentElement and localStorage exist so the theme code can be exercised
// rather than skipped. A fake DOM that omits what the page uses tests a page
// that is not the page.
const THEME_KEY_NAME = "opencli-theme";
const store = {};
global.document.documentElement = new Node("html");
global.window = {
  CSS: null,
  location: {},
  localStorage: {
    getItem: (k) => (k in store ? store[k] : null),
    setItem: (k, v) => { store[k] = String(v); },
  },
};
global.TextDecoder = class { decode() { return ""; } };

// A catalogue shaped exactly like the real one.
const catalogue = {
  binary: "/tmp/opencenter", version: "opencenter version: test", org: "testbench",
  // github-actions-gitops is in the order and first, as it is in the real
  // catalogue. It must be present for the checks to mean anything: without it
  // the rail renders identically whether or not the code that lifts it out of
  // the numbering works.
  stage_order: ["github-actions-gitops", "init", "configure", "validate", "generate",
    "deploy", "operate", "results", "teardown"],
  total_commands: 6,
  environments: [
    {
      id: "openstack", name: "OpenStack", cluster: "tb-openstack", detail: "cloud",
      fixture: "cluster init tb-openstack --org testbench --type openstack",
      commands: [
        { id: "actions-trigger", name: "trigger", task: "actions",
          stage: "github-actions-gitops", short: "Run a GitHub Actions test",
          needs: [], mutating: false, is_group: false, experimental: true,
          ready: "bench actions trigger --approve" },
        { id: "cluster init", name: "init", task: "cluster", stage: "init", short: "Initialize",
          needs: [], mutating: false, is_group: false,
          ready: "cluster init tb-openstack --org testbench --type openstack" },
        { id: "cluster validate", name: "validate", task: "cluster", stage: "validate",
          short: "Validate", needs: [], mutating: false, is_group: false,
          ready: "cluster validate testbench/tb-openstack" },
        { id: "cluster deploy", name: "deploy", task: "cluster", stage: "deploy",
          short: "Deploy", needs: ["the provider"], mutating: true, is_group: false,
          ready: "cluster deploy testbench/tb-openstack" },
        { id: "secrets list", name: "list", task: "secrets", stage: "operate",
          short: "List secrets", needs: [], mutating: false, is_group: false,
          ready: "secrets list" },
        // The journey has to reach its end here too, or the checks cover a
        // workflow that stops before the part that removes real machines.
        { id: "cluster destroy", name: "destroy", task: "cluster", stage: "teardown",
          short: "Destroy", needs: ["the provider"], mutating: true, is_group: false,
          ready: "cluster destroy testbench/tb-openstack --yes" },
      ],
    },
    { id: "kind", name: "Kind", cluster: "tb-kind", detail: "local",
      fixture: "cluster init tb-kind --org testbench --type kind", commands: [] },
  ],
};

const state = {
  catalogue, binary: "/tmp/opencenter", host: "linux/amd64",
  source: {
    repository: "https://github.com/example/openCenter-cli.git",
    branch: "feature/source-picker",
    commit: "1234567890abcdef1234567890abcdef12345678",
    version: "opencenter version: test",
    binary: "/tmp/opencenter",
    ready: true,
  },
  mutate_gate: "OPENCLI_ALLOW_MUTATE", mutate_allowed: false,
  credentials: [{ env: "OS_AUTH_URL", label: "Auth URL", group: "OpenStack", secret: false }],
  saved: {},
  // A failed outcome carries a diagnosis. It is in the fixture because the
  // renderer for it only runs on failures, and a renderer that never runs
  // during a check is a renderer that throws in front of a person instead.
  outcomes: {
    "openstack|cluster validate": {
      exit_code: 1, millis: 120, timed_out: false,
      diagnosis: {
        cause: "cluster demo not found in any organization",
        category: "usage",
        confidence: "likely",
        location: { file: "/tmp/sandbox/clusters/blueprints", line: 383, field: "etcd-backup" },
        possible: [
          { why: "The cluster name is wrong, or the fixture was never created.",
            check: "opencenter cluster list" },
          { why: "A different configuration directory is in use.", check: "echo $OPENCENTER_CONFIG_DIR" },
        ],
      },
    },
  },
  what: [
    { title: "What we test", lead: "Every command this build has.",
      points: ["Does it run?", "Does it fail clearly?"] },
    { title: "Why we test", lead: "Trusted by a person and a pipeline.",
      points: ["Correct", "Safe"] },
    { title: "Where we test", lead: "A sandbox per infrastructure type.",
      points: ["OpenStack", "Kind"] },
    { title: "How we test", lead: "The real binary, output unchanged.",
      points: ["Press Run", "Real exit code"] },
  ],
};

global.fetch = async (url) => ({
  ok: true, status: 200,
  json: async () => (url === "/api/catalogue" ? state :
    url === "/api/results" ? {
      summary: { executed: 0, passed: 0, failed: 0 },
      categories: [], failures: [], matrix: [], causes: [], environments: [],
      environment_statuses: catalogue.environments.map((environment) => ({
        id: environment.id, name: environment.name,
        total: environment.commands.length, executed: 0, passed: 0, failed: 0, blocked: 0,
      })),
      build: {}, cleanup: { note: "" },
    } : {}),
  text: async () => "",
});

(async () => {
  try { eval(script + "\nglobalThis.__streamInto = streamInto;"); } catch (error) {
    fail("the script threw while loading: " + error);
    process.exit(1);
  }
  for (let i = 0; i < 50; i++) await new Promise((r) => setImmediate(r));

  const fallbackOutput = new Node("pre");
  await globalThis.__streamInto({ok: true, status: 200, body: null,
    text: async () => "prerequisite completed\n[exit 0]"}, fallbackOutput);
  if (fallbackOutput.textContent.includes("exit 0")) ok("non-streaming prerequisite output is visible");
  else fail("non-streaming prerequisite output disappeared", fallbackOutput.textContent);
  const errorOutput = new Node("pre");
  await globalThis.__streamInto({ok: false, status: 409, body: null,
    text: async () => "another action is running"}, errorOutput);
  if (errorOutput.textContent.includes("another action is running")) ok("prerequisite HTTP errors are visible");
  else fail("prerequisite HTTP errors disappeared", errorOutput.textContent);

  // All four panels are at the top and populated.
  const strip = byId("strip").textContent;
  for (const wanted of ["What we test", "Why we test", "Where we test", "How we test"]) {
    if (strip.includes(wanted)) ok('"' + wanted + '" is in the top strip');
    else fail('"' + wanted + '" is missing from the top strip');
  }
  if (strip.includes("Does it run?")) ok("the strip renders its points");
  else fail("the strip has no points");

  // Every result is meaningful only for the exact binary named at the top.
  if (byId("ver").textContent.includes("Current Version Tested") &&
      byId("ver").textContent.includes("opencenter version: test") &&
      byId("ver").textContent.includes("GitHub: example/openCenter-cli") &&
      byId("ver").textContent.includes("feature/source-picker") &&
      byId("ver").textContent.includes("1234567890ab") &&
      byId("ver").textContent.includes("Install OpenCLI")) {
    ok("the CLI version, GitHub repository, branch and resolved commit are visible");
  } else {
    fail("the build identity or header install control is missing", byId("ver").textContent);
  }

  // One tab per environment.
  const tabs = byId("envtabs").textContent;
  if (tabs.includes("OpenStack") && tabs.includes("Kind")) ok("an environment tab per infrastructure type");
  else fail("environment tabs missing", tabs);

  // Commands grouped by stage and task.
  const body = byId("commands").textContent;
  for (const wanted of ["init", "validate", "deploy", "operate"]) {
    if (body.includes(wanted)) ok('stage "' + wanted + '" is shown');
    else fail('stage "' + wanted + '" is missing');
  }
  for (const wanted of ["cluster", "secrets"]) {
    if (body.includes(wanted)) ok('task "' + wanted + '" is shown');
    else fail('task "' + wanted + '" is missing');
  }

  // Every command shows its ready-to-run line.
  for (const wanted of [
    "opencenter cluster init tb-openstack",
    "opencenter cluster validate testbench/tb-openstack",
    "opencenter secrets list",
  ]) {
    if (body.includes(wanted)) ok("ready-to-run line: " + wanted);
    else fail("missing ready-to-run line", wanted);
  }

  // A Run button per command.
  const runs = (body.match(/Run/g) || []).length;
  if (runs >= 4) ok("a Run button on every command (" + runs + ")");
  else fail("not every command has a Run button", String(runs));

  // The fixture line for the selected environment.
  if (byId("fixture").textContent.includes("cluster init tb-openstack")) ok("the fixture line is shown");
  else fail("the fixture line is missing");

  // Counts reflect the recorded outcome.
  if (byId("counts").textContent.includes("failed")) ok("results are counted");
  else fail("results are not counted");

  // A failure has to explain itself: what, where, and what usually causes it.
  for (const [what, wanted] of [
    ["the cause", "cluster demo not found in any organization"],
    ["the location", "/tmp/sandbox/clusters/blueprints"],
    ["the offending line", "383"],
    ["a possible cause", "The cluster name is wrong"],
    ["how to check it", "opencenter cluster list"],
  ]) {
    if (body.includes(wanted)) ok("a failed command shows " + what);
    else fail("a failed command does not show " + what, wanted);
  }

  // The order of work has to be stated, not inferred from heading order.
  const flow = byId("flow").textContent;
  for (const [what, wanted] of [
    ["the first step", "init"],
    ["the last step", "teardown"],
    ["what the step does", "Create a cluster definition"],
    ["how you know it worked", "done when:"],
  ]) {
    if (flow.includes(wanted)) ok("the workflow states " + what);
    else fail("the workflow does not state " + what, wanted);
  }
  if (/1[\s\S]*init/.test(flow)) ok("the steps are numbered");
  else fail("the steps are not numbered");
  if (flow.includes("results") && flow.indexOf("results") < flow.indexOf("teardown")) {
    ok("the zero-command Results stage is visible before Teardown");
  } else {
    fail("the Results stage is missing or out of order", flow);
  }
  if (flow.includes("0/0")) ok("the Results stage shows passed/executed before a run");
  else fail("the Results stage does not show 0/0 before a run", flow);

  // GitHub Actions is above the rail, not a step inside it.
  //
  // It ran the whole rail while sitting in the middle of it numbered "2",
  // which read as "do this after prerequisites and before init". Two things
  // must hold: it renders into its own strip, and the numbered steps below do
  // not include it — a version that skipped it after incrementing the counter
  // would leave the work numbered 1, 3, 4.
  const top = byId("flow-top").textContent;
  if (top.includes("github-actions-gitops")) {
    ok("GitHub Actions sits in the strip above the rail");
  } else {
    fail("GitHub Actions is not in the strip above the rail", top);
  }
  if (top.includes("runs every stage below")) {
    ok("the strip says it runs the rail below it");
  } else {
    fail("the strip does not say what it does to the rail", top);
  }
  if (!flow.includes("github-actions-gitops")) {
    ok("GitHub Actions is not also a numbered step");
  } else {
    fail("GitHub Actions appears twice: in the rail and above it", flow);
  }
  // The stage sits first in stage_order, so if it were skipped after the
  // counter moved the work below would start at 2.
  if (/1[\s\S]*init[\s\S]*2[\s\S]*validate/.test(flow)) {
    ok("the rail numbering has no gap where it used to sit");
  } else {
    fail("the rail numbering skips a number", flow);
  }

  // Both result areas stay visible before the first run, with placeholders
  // for every field rather than appearing only after a command finishes.
  const summary = byId("tsum").textContent;
  const detail = byId("triage").textContent;
  // The board is the same board before and after the first run.
  //
  // There were two layouts here: a row of dashed counters before anything ran,
  // and the dashboard afterwards — so the thing a newcomer saw first was the
  // one thing that was not the dashboard. These assertions are what stops that
  // coming back: every part of the board must be present with nothing run.
  for (const label of ["Local results", "build health", "Environments",
                       "Next actions", "passed", "failed", "regressions",
                       "OpenStack", "Kind"]) {
    if (summary.includes(label)) ok("the empty board still shows " + label);
    else fail("the empty board is missing " + label, summary);
  }
  // Where the numbers came from. Two results sections that can disagree have
  // to say which machine produced each.
  if (summary.includes("run on this machine")) {
    ok("the board says where the local numbers came from");
  } else {
    fail("the board does not say where its numbers came from", summary);
  }
  // A dash, not 0%: nothing ran is not the same claim as everything failed.
  if (summary.includes("—") && summary.includes("Nothing has run")) {
    ok("an unrun board reads as unrun rather than as failing");
  } else {
    fail("an unrun board does not distinguish itself from a failing one", summary);
  }
  // The board is named, and its GitHub half is reachable.
  //
  // The CI host was built with a class where the code that fills it looks up an
  // id, so the lookup returned null and the entire GitHub Actions section never
  // appeared — silently, because an optional panel that draws nothing looks the
  // same as one nobody configured.
  if (summary.includes("Test Results Summary")) {
    ok("the board says what it is");
  } else {
    fail("the results board has no name", summary);
  }
  // Walked, not looked up: byId here creates whatever is asked for, so asking
  // it would answer yes even when the board never appended the host at all —
  // which is precisely the bug.
  const findById = (node, wanted) => {
    if (!node || typeof node !== "object") return null;
    if (node.id === wanted) return node;
    for (const child of node.children || []) {
      const hit = findById(child, wanted);
      if (hit) return hit;
    }
    return null;
  };
  if (findById(byId("tsum"), "tsum-ci")) {
    ok("the GitHub Actions half has a host the loader can find");
  } else {
    fail("the GitHub Actions section can never be filled in",
      "refreshActionsBoard looks up #tsum-ci by id, and the board appended no such node");
  }

  // The two ways to run it, offered where the actions go.
  if (summary.includes("Press Run on any command") &&
      summary.includes("GitHub Actions panel")) {
    ok("the empty board offers both ways to run the bench");
  } else {
    fail("the empty board does not say how to fill itself in", summary);
  }
  if (detail.includes("Test results") && detail.includes("Nothing has run yet")) {
    ok("the detailed Results section is visible before a run");
  } else {
    fail("the detailed Results section is missing before a run", detail);
  }
  for (const label of ["Command", "Environment", "Build", "Exit code", "Duration",
                       "Category", "Probable cause", "stdout", "stderr", "Reproduce"]) {
    if (detail.includes(label)) ok("empty detailed results show " + label);
    else fail("empty detailed results are missing " + label, detail);
  }
  if ((detail.match(/—/g) || []).length >= 10) ok("detailed results use dash placeholders");
  else fail("detailed results do not show all dash placeholders", detail);

  if (!html.includes("Preflight &amp; Prerequisites") && !/id="cta"/.test(html)) {
    ok("there is no duplicate full-test button or preflight dashboard");
  } else {
    fail("a duplicate full-test button or preflight dashboard is present");
  }

  // A fallback --fill written after the named stages has the same specificity
  // and wins over all of them, turning every stage grey. It has happened
  // twice, both times found by looking at a screenshot rather than by any
  // check, so the ordering is asserted here.
  const css = html.split("<style>")[1].split("</style>")[0];
  if (/\.environment-block>\.block-title\s*\{[^}]*background:linear-gradient[^}]*font-size:16px/.test(css) &&
      /class="block environment-block"/.test(html)) {
    ok("the Environment section has a large coloured header");
  } else {
    fail("the Environment section header is not large and coloured");
  }
  const fallbackAt = css.search(/\.step\s*,\s*\.stage\s*\{[^}]*--fill/);
  const firstNamedAt = css.search(/\[data-stage="init"\]\s*\{[^}]*--fill/);
  if (fallbackAt < 0 || firstNamedAt < 0) {
    fail("could not find the stage fill rules", "the check needs updating");
  } else if (fallbackAt < firstNamedAt) {
    ok("the stage fill fallback is declared before the named stages");
  } else {
    fail("the stage fill fallback is declared AFTER the named stages",
      "same specificity, so it wins and every stage renders grey");
  }

  // The theme toggle has to switch, label itself by where it goes, and
  // remember. Pressing it is the only way to know it is wired.
  const themeButton = byId("theme");
  const root = global.document.documentElement;
  if (!themeButton.listeners.click || themeButton.listeners.click.length === 0) {
    fail("the theme button has no click handler", "it renders and does nothing");
  } else {
    const before = themeButton._text;
    themeButton.click();
    const light = root.attributes["data-theme"];
    if (light === "light") ok("pressing the toggle switches to the light theme");
    else fail("pressing the toggle did not switch to light", String(light));

    if (themeButton._text !== before) ok('the button relabels itself ("' + themeButton._text + '")');
    else fail("the button label did not change", before);

    if (store[THEME_KEY_NAME] === "light") ok("the choice is remembered");
    else fail("the choice was not stored", JSON.stringify(store));

    themeButton.click();
    if (!root.attributes["data-theme"]) ok("pressing it again returns to dark");
    else fail("it did not switch back", String(root.attributes["data-theme"]));
  }

  // How to use the thing, before any of what it is.
  const howto = byId("howto").textContent;
  for (const [what, wanted] of [
    ["how to pick an environment", "Pick where you are testing"],
    ["where credentials go", "credentials"],
    ["that Run is the thing to press", "Press Run"],
    ["that output appears below the row", "Read the output"],
    ["how to export", "Export the results"],
  ]) {
    if (howto.includes(wanted)) ok("the how-to explains " + what);
    else fail("the how-to does not explain " + what, wanted);
  }

  // The two explanation columns must be labelled. They were not, and a reader
  // had to work out what they were from the contents.
  for (const [what, wanted] of [
    ["the plain-language column", "what it does"],
    ["the metaphor column", "like building a city"],
  ]) {
    if (body.includes(wanted)) ok(what + " has a title");
    else fail(what + " has no title", wanted);
  }

  // Credentials open by default: a panel that must be found before the
  // provider commands work is not a panel to hide.
  if (/id="cred-fold"[^>]*\sopen/.test(html)) ok("credentials are open by default");
  else fail("credentials are collapsed by default", "they have to be found before anything provider-backed runs");

  // Both ways of running the bench have to be stated. The six numbered steps
  // are one of them, and a reader who takes them for the only one presses
  // every command by hand forever.
  for (const [what, wanted] of [
    ["that there are two ways to run it", "Two ways to run it"],
    ["that GitHub can trigger it", "Or let GitHub trigger it"],
    ["that CI runs the same commands", "same commands as the buttons below"],
  ]) {
    if (howto.includes(wanted)) ok("the how-to explains " + what);
    else fail("the how-to does not explain " + what, wanted);
  }

  // The Actions panel governs what a CI run does, so it goes above the
  // environment and credentials it carries into the generated workflow.
  const at3 = (needle) => html.indexOf(needle);
  if (at3('id="actions-block"') > 0 &&
      at3('id="actions-block"') < at3('class="block environment-block"')) {
    ok("the GitHub Actions panel is above the Environment panel");
  } else {
    fail("the GitHub Actions panel is not above the Environment panel",
      "it is the run those settings are carried into");
  }

  // The environment bar decides what the credentials panel contains, so it
  // belongs above it.
  const at2 = (needle) => html.indexOf(needle);
  if (at2('class="envbar"') > 0 && at2('class="envbar"') < at2('id="cred-fold"')) {
    ok("the environment bar is above the credentials");
  } else {
    fail("the environment bar is not above the credentials",
      "it decides what that panel contains");
  }

  // Credentials are a prerequisite, so they must be above the command table.
  const at = (needle) => html.indexOf(needle);
  if (at('id="cred-fold"') > 0 && at('id="cred-fold"') < at('id="commands"')) {
    ok("credentials come before the command table");
  } else {
    fail("credentials are not above the command table",
      "a person cannot run the provider commands without finding them first");
  }

  console.log();
  if (failures === 0) console.log("  the page works");
  else { console.log("  " + failures + " problem(s)"); process.exit(1); }
})();
