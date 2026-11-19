// Browser tests against a running sandbox container whose address is in
// SANDBOX_URL; the tests that inspect processes also need SANDBOX_CONTAINER.
const { defineConfig } = require("@playwright/test");

if (!process.env.SANDBOX_URL)
  throw new Error("SANDBOX_URL is not set; the UI tests run against a running container (see .github/workflows/ci.yml)");

module.exports = defineConfig({
  testDir: ".",
  testMatch: "*.spec.js",
  // One run at a time: a test that checks the container's processes must
  // not see another test's run.
  workers: 1,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  reporter: [["list"]],
  use: { baseURL: process.env.SANDBOX_URL, browserName: "chromium" },
});
