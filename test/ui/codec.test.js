// Unit tests for web/codec.js, the permalink and output helpers that
// frontend.js uses. Run with: node --test test/ui/codec.test.js
const test = require("node:test");
const assert = require("node:assert/strict");

globalThis.pako = require("../../web/pako.min.js");
const codec = require("../../web/codec.js");
const fixtures = require("./fixtures.js");

test("a permalink round-trips language, header, code, footer, input and arguments", () => {
  const fields = ["apl-dyalog", "h←1", "⎕←h+⍳3", "⍝ end", "line 1\nline 2", "first arg", "second arg"];

  const fragment = codec.fieldsToPermalink(fields);

  assert.match(fragment, /^##[A-Za-z0-9@/]+$/);
  assert.deepEqual(codec.parsePermalink(fragment.slice(1)), { language: "apl-dyalog", fields });
});

test("a permalink captured from tio.run decodes to the fields it was made from", () => {
  const f = fixtures.aplDyalog;

  const parsed = codec.parsePermalink(f.fragment.slice(1));

  assert.deepEqual(parsed, {
    language: f.language,
    fields: [f.language, f.header, f.code, f.footer, f.input, ...f.args],
  });
});

test("a permalink for another language names that language", () => {
  const parsed = codec.parsePermalink(fixtures.javascript.fragment.slice(1));

  assert.equal(parsed.language, "javascript-v8");
});

test("a bare language id is a language without fields", () => {
  assert.deepEqual(codec.parsePermalink("apl-dyalog"), { language: "apl-dyalog", fields: null });
});

test("CR and CRLF line endings in output become line breaks", () => {
  assert.equal(codec.displayText("first\rsecond\r"), "first\nsecond\n");
  assert.equal(codec.displayText("first\r\nsecond"), "first\nsecond");
  assert.equal(codec.displayText("first\nsecond"), "first\nsecond");
});
