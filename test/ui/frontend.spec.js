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

test("opening / shows the Dyalog interpreter page with TIO's sections and no language list", async ({ page }) => {
  await page.goto("/");

  await expect(page.locator("#interpreter")).toBeVisible();
  await expect(page.locator("#lang-name")).toHaveText("Dyalog");
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
  expect(formats[4]).toMatch(/^# \[Dyalog APL\], \d+ bytes/);

  const reopened = await context.newPage();
  await reopened.goto(url);
  await expect(reopened.locator("#header")).toHaveValue("h←1");
  await expect(reopened.locator("#code")).toHaveValue("⎕←h+⍳3");
  await expect(reopened.locator("#footer")).toHaveValue("⍝ end");
  await expect(reopened.locator("#input")).toHaveValue("line 1\nline 2");
  const args = reopened.locator("#cla-wrapper textarea");
  await expect(args).toHaveCount(2);
  await expect(args.nth(0)).toHaveValue("first arg");
  await expect(args.nth(1)).toHaveValue("second arg");
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

test("a permalink for any other language shows that the language could not be found, without advice to refresh", async ({ page }) => {
  await page.goto("/" + fixtures.javascript.fragment);

  const message = page.locator("#messages .message", { hasText: "The requested language could not be found" });
  await expect(message).toBeVisible();
  await expect(message).not.toContainText(/refresh|cach/i);
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

test("the page title is dyalog.run whatever the permalink", async ({ page }) => {
  await page.goto("/" + fixtures.aplDyalog.fragment);
  await expect(page.locator("#code")).toHaveValue(fixtures.aplDyalog.code);

  await expect(page).toHaveTitle("dyalog.run");
});

test("the navigation bar has the Run and permalink buttons and the APL Wiki button, and Run shows the logo", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("#interpreter")).toBeVisible();

  expect(await page.locator("nav > *:visible").evaluateAll(buttons => buttons.map(b => b.id))).toEqual(["run", "permalink", "wiki"]);
  await expect(page.locator('#run svg path[d^="M0 0L90.9989"]')).toBeVisible();
  await expect(page.getByRole("link", { name: "dyalog.run" })).toHaveCount(0);
});

test("opening a permalink runs its program without any input", async ({ page }) => {
  await page.goto("/" + fixtures.aplDyalog.fragment);

  await expect(page.locator("#output")).toHaveValue(fixtures.aplDyalog.output);
});

test("opening a page without a program makes no run request", async ({ page }) => {
  const runs = [];
  page.on("request", request => {
    if (new URL(request.url()).pathname === "/api/v1/run")
      runs.push(request.url());
  });

  for (const path of ["/", "/#apl-dyalog"]) {
    await page.goto(path);
    await expect(page.locator("#interpreter")).toBeVisible();
    await page.waitForTimeout(1000);
  }

  expect(runs).toEqual([]);
  await expect(page.locator("#output")).toHaveValue("");
});

// runButton returns an image of the Run button, with the pointer elsewhere
// so that the image shows no hover colour.
async function runButton(page) {
  await page.mouse.move(5, 600);
  return await page.locator("#run").screenshot();
}

// startLongRun opens a new program that waits three seconds, and returns
// the Run button's image before the run starts.
async function startLongRun(page) {
  await page.goto("/");
  await page.fill("#code", "⎕DL 3");
  await page.waitForTimeout(500);
  const idle = await runButton(page);
  await page.locator("#code").press("Control+Enter");
  await expect(page.locator("#run")).toHaveClass(/running/);
  return idle;
}

test("while a run is in progress the Run button shows a spinning cogwheel, and the D again when it finishes", async ({ page }) => {
  const idle = await startLongRun(page);

  await page.waitForTimeout(1200);
  const first = await runButton(page);
  await page.waitForTimeout(300);
  const second = await runButton(page);

  expect(first.equals(idle)).toBe(false);
  expect(second.equals(first)).toBe(false);
  await expect(page.locator("#run")).not.toHaveClass(/running/, { timeout: 10_000 });
  await expect.poll(async () => (await runButton(page)).equals(idle), { timeout: 3_000 }).toBe(true);
});

test("an interrupted run turns the cogwheel back into the D", async ({ page }) => {
  const idle = await startLongRun(page);
  await page.waitForTimeout(1200);
  expect((await runButton(page)).equals(idle)).toBe(false);

  await page.click("#run");

  await expect(page.locator("#run")).not.toHaveClass(/running/, { timeout: 2_000 });
  await expect.poll(async () => (await runButton(page)).equals(idle), { timeout: 3_000 }).toBe(true);
});

test("with reduced motion the cogwheel shows while running but does not spin", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  const idle = await startLongRun(page);

  await page.waitForTimeout(1200);
  const first = await runButton(page);
  await page.waitForTimeout(300);
  const second = await runButton(page);

  expect(first.equals(idle)).toBe(false);
  expect(second.equals(first)).toBe(true);
  await expect(page.locator("#run")).not.toHaveClass(/running/, { timeout: 10_000 });
  await expect.poll(async () => (await runButton(page)).equals(idle), { timeout: 3_000 }).toBe(true);
});

test("the language bar sits above the navigation bar", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("#interpreter")).toBeVisible();
  const bar = page.locator(".ngn_lb");
  const nav = page.locator("nav");

  expect((await bar.boundingBox()).y).toBe(0);
  await expect.poll(async () => {
    const b = await bar.boundingBox(), n = await nav.boundingBox();
    return Math.abs(n.y - (b.y + b.height)) <= 1;
  }).toBe(true);
  const n = await nav.boundingBox();
  expect((await page.locator("#interpreter h2").boundingBox()).y).toBeGreaterThanOrEqual(n.y + n.height);
});

test("the language bar has no close button", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".ngn_lb b").first()).toBeVisible();

  await expect(page.locator(".ngn_lb [title=Close]")).toHaveCount(0);
  await expect(page.locator(".ngn_lb")).not.toContainText("\u274e");
});

test("code fields and the language bar use the APL387 font", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".ngn_lb b").first()).toBeVisible();

  for (const selector of ["#code", "#debug", ".ngn_lb"])
    expect(await page.locator(selector).evaluate(e => getComputedStyle(e).fontFamily)).toMatch(/^"?APL387"?(,|$)/);
  await expect.poll(() => page.evaluate(() =>
    [...document.fonts].some(face => face.family.replace(/"/g, "") === "APL387" && face.status === "loaded"))).toBe(true);
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
  await expect(page.locator("#output")).toHaveValue(fixtures.aplDyalog.output);
  await page.click("#permalink");

  expect(foreign).toEqual([]);
});

// errorMessage returns the visible message whose title is the given text.
function errorMessage(page, title) {
  return page.locator("#messages .message:visible")
    .filter({ has: page.locator("h4", { hasText: new RegExp(`^${title}$`) }) });
}

test("a run whose request gets no response ends with an error once the server's time limit and a margin have passed", async ({ page }) => {
  await page.route("**/api/v1/version", async route => {
    const response = await route.fetch();
    await route.fulfill({ response, json: { ...(await response.json()), timeoutMs: 2000 } });
  });
  await page.route("**/api/v1/run", () => {});
  await page.goto("/");
  await expect(page.locator("#lang-version")).not.toBeEmpty();
  await page.fill("#code", "⎕←2+2");
  await page.waitForTimeout(500);
  const idle = await runButton(page);

  await page.locator("#code").press("Control+Enter");

  await page.waitForTimeout(2000 + 5000);
  await expect(page.locator("#run")).toHaveClass(/running/);
  await expect(errorMessage(page, "Error")).toContainText("did not respond", { timeout: 8_000 });
  await expect(page.locator("#run")).not.toHaveClass(/running/);
  await expect.poll(async () => (await runButton(page)).equals(idle), { timeout: 3_000 }).toBe(true);
});

for (const [kind, body] of [
  ["not JSON", "<html><body>Welcome</body></html>"],
  ["cut short", '{"stdout":"4\\n","stderr":"","exitCo'],
  ["JSON but not a run result", '{"status":"ok"}'],
])
  test(`a 200 response that is ${kind} ends the run with an error saying the response could not be read`, async ({ page }) => {
    await page.route("**/api/v1/run", route => route.fulfill({ status: 200, contentType: "application/json", body }));
    await page.goto("/");
    await page.fill("#code", "⎕←2+2");

    await page.locator("#code").press("Control+Enter");

    await expect(errorMessage(page, "Error")).toContainText("response could not be read");
    await expect(page.locator("#run")).not.toHaveClass(/running/);
  });

for (const [kind, contentType, body] of [
  ["an empty body", "text/plain", ""],
  ["an HTML page", "text/html", "<html><body><h1>502 Bad Gateway</h1></body></html>"],
])
  test(`an error status with ${kind} shows the status and a description without markup`, async ({ page }) => {
    await page.route("**/api/v1/run", route => route.fulfill({ status: 502, contentType, body }));
    await page.goto("/");
    await page.fill("#code", "⎕←2+2");

    await page.locator("#code").press("Control+Enter");

    await expect(errorMessage(page, "Error 502").locator("div")).toHaveText("The server could not run the program.");
  });

test("an error status with a plain-text reason shows the status and the reason", async ({ page }) => {
  await page.route("**/api/v1/run", route => route.fulfill({ status: 429, contentType: "text/plain; charset=utf-8", body: "every run slot is busy\n" }));
  await page.goto("/");
  await page.fill("#code", "⎕←2+2");

  await page.locator("#code").press("Control+Enter");

  await expect(errorMessage(page, "Error 429").locator("div")).toHaveText("every run slot is busy");
});

for (const [kind, answer] of [
  ["an unreadable response", route => route.fulfill({ status: 200, contentType: "application/json", body: "garbage" })],
  ["an error status", route => route.fulfill({ status: 429, contentType: "text/plain", body: "every run slot is busy" })],
  ["a connection failure", route => route.abort("connectionreset")],
])
  test(`after a run that ends with ${kind}, Output and Debug no longer show the previous run's results`, async ({ page }) => {
    await page.goto("/");
    await page.fill("#code", "⎕←2+2\n1÷0");
    await page.locator("#code").press("Control+Enter");
    await expect(page.locator("#output")).toHaveValue("4\n");
    await expect(page.locator("#debug")).toHaveValue(/DOMAIN ERROR/);
    await page.route("**/api/v1/run", answer);

    await page.locator("#code").press("Control+Enter");

    await expect(page.locator("#messages .message:visible h4", { hasText: /^Error/ })).toHaveCount(1);
    await expect(page.locator("#output")).toHaveValue("");
    await expect(page.locator("#debug")).toHaveValue("");
  });

test("a run the user cancels leaves Output and Debug as they were", async ({ page }) => {
  await page.goto("/");
  await page.fill("#code", "⎕←2+2\n1÷0");
  await page.locator("#code").press("Control+Enter");
  await expect(page.locator("#output")).toHaveValue("4\n");
  await expect(page.locator("#debug")).toHaveValue(/DOMAIN ERROR/);
  const debug = await page.locator("#debug").inputValue();
  await page.fill("#code", "⎕DL 30");
  await page.locator("#code").press("Control+Enter");
  await expect(page.locator("#run")).toHaveClass(/running/);
  await page.waitForTimeout(500);

  await page.click("#run");

  await expect(page.locator("#run")).not.toHaveClass(/running/);
  await expect(page.locator("#output")).toHaveValue("4\n");
  await expect(page.locator("#debug")).toHaveValue(debug);
});

test("the page requests no language list", async ({ page }) => {
  const paths = [];
  page.on("request", request => paths.push(new URL(request.url()).pathname));

  await page.goto("/");
  await expect(page.locator("#interpreter")).toBeVisible();
  await page.waitForLoadState("networkidle");

  expect(paths.filter(path => /languages/.test(path))).toEqual([]);
});

test("the Hello World example fills the code with Dyalog's Hello World program", async ({ page }) => {
  await page.goto("/");

  await page.click("#lang-example");

  await expect(page.locator("#code")).toHaveValue("⎕←'Hello, World!'");
});

test("the code's size is shown in characters and UTF-8 bytes", async ({ page }) => {
  await page.goto("/");

  await page.fill("#code", "⎕←1");

  await expect(page.locator("#code-info")).toHaveText("3 chars, 7 bytes (UTF-8)");
});

test("opening a permalink whose input has no final line break reads that input as a complete line", async ({ page }) => {
  await page.goto("/" + fixtures.readInput.fragment);
  await expect(page.locator("#code")).toHaveValue(fixtures.readInput.code);
  await expect(page.locator("#input")).toHaveValue(fixtures.readInput.input);

  await expect(page.locator("#output")).toHaveValue("hi\n");
  await expect(page.locator("#debug")).not.toHaveValue(/EOF INTERRUPT/);
});

for (const [kind, input, sent] of [
  ["without a final line break is sent with one", "a\nb", "a\nb\n"],
  ["that ends with a line break is sent unchanged", "a\nb\n", "a\nb\n"],
  ["that is empty is sent empty", "", ""],
])
  test(`input ${kind}`, async ({ page }) => {
    const bodies = [];
    page.on("request", request => {
      if (new URL(request.url()).pathname === "/api/v1/run")
        bodies.push(request.postDataJSON());
    });
    await page.goto("/");
    await page.fill("#code", "⎕←1");
    if (input) {
      await openSection(page, "input");
      await page.fill("#input", input);
    }

    await page.locator("#code").press("Control+Enter");

    await expect.poll(() => bodies.length).toBe(1);
    expect(bodies[0].input).toBe(sent);
    await expect(page.locator("#input")).toHaveValue(input);
  });

test("the page's styles all come from its stylesheet", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".ngn_lb b").first()).toBeVisible();

  await expect(page.locator("style")).toHaveCount(0);
  expect(await page.evaluate(() => document.body.getAttribute("style"))).toBeNull();
});

test("the page defines no fonts other than APL335 and APL387", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("#interpreter")).toBeVisible();

  const families = await page.evaluate(() => [...new Set([...document.fonts].map(face => face.family.replace(/"/g, "")))].sort());
  expect(families).toEqual(["APL335", "APL387"]);
});

test("text outside the code fields and the language bar uses the APL335 font", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("#interpreter")).toBeVisible();

  for (const selector of ["body", "#lang-name", "#lang-version", "#interpreter h3", "#lang-example span", ".array-add span", "#code-info"])
    expect(await page.locator(selector).first().evaluate(e => getComputedStyle(e).fontFamily), selector).toMatch(/^"?APL335"?(,|$)/);
  await expect.poll(() => page.evaluate(() =>
    [...document.fonts].some(face => face.family.replace(/"/g, "") === "APL335" && face.status === "loaded"))).toBe(true);
});

// emphasis returns the weight, text stroke width and letter spacing of the
// first element that matches the selector.
async function emphasis(page, selector) {
  return await page.locator(selector).first().evaluate(e => {
    const style = getComputedStyle(e);
    return { weight: style.fontWeight, stroke: parseFloat(style.webkitTextStrokeWidth), spacing: parseFloat(style.letterSpacing) || 0 };
  });
}

test("bold text has normal weight, a text stroke and letter spacing; plain text in a heading has neither", async ({ page }) => {
  await page.goto("/" + fixtures.javascript.fragment);
  await expect(page.locator("#messages .message:visible h4")).toBeVisible();
  const bold = { "error title": await emphasis(page, "#messages .message:visible h4") };
  await page.goto("/");
  await expect(page.locator("#interpreter")).toBeVisible();
  for (const selector of ["#interpreter h2", "#interpreter h3", ".array-add span"])
    bold[selector] = await emphasis(page, selector);

  for (const [name, style] of Object.entries(bold)) {
    expect(style.weight, name).toBe("400");
    expect(style.stroke, name).toBeGreaterThan(0);
    expect(style.spacing, name).toBeGreaterThan(0);
  }
  expect(await emphasis(page, "#lang-version")).toEqual({ weight: "400", stroke: 0, spacing: 0 });
});

// box returns an element's position and size, rounded to whole pixels.
async function box(locator) {
  const b = await locator.boundingBox();
  return { x: Math.round(b.x), y: Math.round(b.y), width: Math.round(b.width), height: Math.round(b.height) };
}

for (const [device, viewport] of [["a desktop", { width: 1280, height: 720 }], ["a phone", { width: 360, height: 640 }]])
  test(`on ${device}, the APL Wiki button sits at the right end of the navigation bar, level with and the size of the Run button`, async ({ page }) => {
    await page.setViewportSize(viewport);
    await page.goto("/");
    await expect(page.locator("#wiki")).toBeVisible();

    const run = await box(page.locator("#run")), permalink = await box(page.locator("#permalink"));
    const wiki = await box(page.locator("#wiki")), nav = await box(page.locator("nav"));
    expect({ y: wiki.y, width: wiki.width, height: wiki.height }).toEqual({ y: run.y, width: run.width, height: run.height });
    expect(wiki.x + wiki.width).toBe(nav.x + nav.width);
    expect(wiki.x).toBeGreaterThan(permalink.x + permalink.width);
  });

test("the APL Wiki button shows the APL logo in the colours of the Run button", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("#interpreter")).toBeVisible();
  await page.mouse.move(5, 600);

  const logo = page.locator('#wiki svg path[d^="M1837.93 1692.64"]');
  await expect(logo).toBeVisible();
  const colours = async (button, path) => ({
    background: await page.locator(button).evaluate(e => getComputedStyle(e).backgroundColor),
    fill: await page.locator(path).evaluate(e => getComputedStyle(e).fill),
    height: Math.round((await page.locator(button + " svg").boundingBox()).height),
  });
  expect(await colours("#wiki", '#wiki svg path[d^="M1837.93 1692.64"]')).toEqual(await colours("#run", "#run-d"));
});

// stubWiki answers requests for the APL Wiki, so that the tests do not
// depend on another site.
async function stubWiki(context) {
  await context.route("https://aplwiki.com/**", route => route.fulfill({ contentType: "text/html", body: "<title>APL Wiki</title>" }));
}

test("clicking the APL Wiki button opens the APL Wiki in a new tab, leaving the program open", async ({ page, context }) => {
  await stubWiki(context);
  await page.goto("/");
  await expect(page.locator("#wiki")).toBeVisible();
  await page.fill("#code", "⎕←1");

  const [tab] = await Promise.all([context.waitForEvent("page"), page.click("#wiki")]);

  await tab.waitForLoadState();
  expect(tab.url()).toBe("https://aplwiki.com/");
  expect(page.url()).not.toContain("aplwiki");
  await expect(page.locator("#code")).toHaveValue("⎕←1");
});

test("Escape then W opens the APL Wiki in a new tab", async ({ page, context }) => {
  await stubWiki(context);
  await page.goto("/");
  await expect(page.locator("#wiki")).toBeVisible();
  await page.locator("#code").click();

  const [tab] = await Promise.all([context.waitForEvent("page"), (async () => {
    await page.keyboard.press("Escape");
    await page.keyboard.press("w");
  })()]);

  await tab.waitForLoadState();
  expect(tab.url()).toBe("https://aplwiki.com/");
});
