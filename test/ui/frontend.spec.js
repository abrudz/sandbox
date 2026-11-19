// Browser tests for the frontend (web/), TIO's apl-dyalog page adapted to
// the run API.
const { test, expect } = require("@playwright/test");
const { execFileSync } = require("node:child_process");
const fixtures = require("./fixtures.js");

// openSection expands a collapsed TIO section (header, footer, input, arguments).
async function openSection(page, name) {
  if (!(await page.locator(`#toggle-${name}`).isChecked()))
    await page.click(`label[for=toggle-${name}]`);
}

// addArgument adds one command-line argument field and fills it.
async function addArgument(page, value) {
  await page.click("#cla-wrapper .array-tail");
  await page.locator("#cla-wrapper textarea").last().fill(value);
}

// interpreterRunning reports whether a dyalog process exists in the container.
function interpreterRunning() {
  const container = process.env.SANDBOX_CONTAINER;
  if (!container) throw new Error("SANDBOX_CONTAINER is not set");
  const names = execFileSync("docker", ["exec", container, "sh", "-c", "cat /proc/[0-9]*/comm 2>/dev/null"], { encoding: "utf8" });
  return names.split("\n").includes("dyalog");
}

test("opening / shows the apl-dyalog interpreter page with TIO's sections and no language list", async ({ page }) => {
  await page.goto("/");

  await expect(page.locator("#interpreter")).toBeVisible();
  await expect(page.locator("#lang-name")).toHaveText("APL (Dyalog Unicode)");
  for (const section of ["Header", "Code", "Footer", "Input", "Arguments", "Output", "Debug"])
    await expect(page.locator("#interpreter h3 label span", { hasText: new RegExp(`^${section}$`) })).toBeVisible();
  await expect(page.locator("#results, #search, #lang-switch")).toHaveCount(0);
});

test("Ctrl-Enter runs the program; Output shows stdout and Debug shows stderr with TIO's timing lines", async ({ page }) => {
  await page.goto("/");
  await page.fill("#code", "⎕←2+2\n1÷0");

  await page.locator("#code").press("Control+Enter");

  await expect(page.locator("#output")).toHaveValue("4\n");
  const debug = page.locator("#debug");
  await expect(debug).toHaveValue(/DOMAIN ERROR/);
  await expect(debug).toHaveValue(/\nReal time: \d+\.\d{3} s\nUser time: \d+\.\d{3} s\nSys\. time: \d+\.\d{3} s\nCPU share: \d+\.\d{2} %\nExit code: [1-9]\d*$/);
});

test("clicking Run during a run cancels it, and the server stops the run", async ({ page }) => {
  await page.goto("/");
  await page.fill("#code", "⎕DL 30");
  await page.click("#run");
  await expect(page.locator("#run")).toHaveClass(/running/);
  await expect.poll(interpreterRunning, { timeout: 10_000 }).toBe(true);

  await page.click("#run");

  await expect(page.locator("#run")).not.toHaveClass(/running/, { timeout: 2_000 });
  await expect.poll(interpreterRunning, { timeout: 2_000 }).toBe(false);
});

test("each permalink format is generated, and reopening the permalink restores every field", async ({ page, context }) => {
  await page.goto("/");
  await openSection(page, "header");
  await page.fill("#header", "h←1");
  await page.fill("#code", "⎕←h+⍳3");
  await openSection(page, "footer");
  await page.fill("#footer", "⍝ end");
  await openSection(page, "input");
  await page.fill("#input", "line 1\nline 2");
  await openSection(page, "arguments");
  await addArgument(page, "first arg");
  await addArgument(page, "second arg");

  await page.click("#permalink");

  const url = page.url();
  expect(url).toContain("##");
  const formats = await page.locator("#permalink-drawer textarea").evaluateAll(areas => areas.map(a => a.value));
  expect(formats).toHaveLength(6);
  expect(formats[0]).toBe(url);
  for (const format of formats) {
    expect(format).toContain(url);
    expect(format).not.toMatch(/try it online|tio\.run/i);
  }
  expect(formats[4]).toMatch(/^# \[APL \(Dyalog Unicode\)\], \d+ bytes/);

  const reopened = await context.newPage();
  await reopened.goto(url);
  await expect(reopened.locator("#header")).toHaveValue("h←1");
  await expect(reopened.locator("#code")).toHaveValue("⎕←h+⍳3");
  await expect(reopened.locator("#footer")).toHaveValue("⍝ end");
  await expect(reopened.locator("#input")).toHaveValue("line 1\nline 2");
  await expect(reopened.locator("#cla-wrapper textarea")).toHaveValues(["first arg", "second arg"]);
});

test("a tio.run permalink for apl-dyalog opens with the same fields when its host is replaced", async ({ page }) => {
  const f = fixtures.aplDyalog;

  await page.goto("/" + f.fragment);

  await expect(page.locator("#header")).toHaveValue(f.header);
  await expect(page.locator("#code")).toHaveValue(f.code);
  await expect(page.locator("#footer")).toHaveValue(f.footer);
  await expect(page.locator("#input")).toHaveValue(f.input);
  await expect(page.locator("#cla-wrapper textarea")).toHaveCount(0);
});

test("a permalink for any other language shows TIO's language-not-found message", async ({ page }) => {
  await page.goto("/" + fixtures.javascript.fragment);

  await expect(page.locator("#messages .message", { hasText: "The requested language could not be found" })).toBeVisible();
  await expect(page.locator("#interpreter")).toBeHidden();
});

test("CR line endings in output display as line breaks", async ({ page }) => {
  await page.goto("/");
  await page.fill("#code", "⎕←'first'\n⎕←'second'");

  await page.locator("#code").press("Control+Enter");

  await expect(page.locator("#output")).toHaveValue("first\nsecond\n");
});

test("APL glyphs can be entered through the language bar and backtick key bindings", async ({ page }) => {
  await page.goto("/");
  const code = page.locator("#code");
  await code.click();

  await page.keyboard.press("`");
  await page.keyboard.press("r");
  await page.locator(".ngn_lb b", { hasText: /^⍳$/ }).click();

  await expect(code).toHaveValue("⍴⍳");
});

test("the page shows the Dyalog version", async ({ page, request }) => {
  const version = await (await request.get("/api/v1/version")).json();

  await page.goto("/");

  await expect(page.locator("#lang-version")).toHaveText(version.dyalogVersion);
});

test("the page makes no requests to any origin other than its own", async ({ page, baseURL }) => {
  const origin = new URL(baseURL).origin;
  const foreign = [];
  page.on("request", request => {
    const url = request.url();
    if (!url.startsWith("data:") && new URL(url).origin !== origin)
      foreign.push(url);
  });

  await page.goto("/" + fixtures.aplDyalog.fragment);
  await page.locator("#code").press("Control+Enter");
  await expect(page.locator("#output")).not.toHaveValue("");
  await page.click("#permalink");

  expect(foreign).toEqual([]);
});
