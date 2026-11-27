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

// permalinkFields returns the fields of an apl-dyalog permalink with code
// and arguments.
function permalinkFields(code, args) {
  return ["apl-dyalog", "", code, "", "", ...args];
}

test("a permalink at runner-api's limits opens with every field intact", () => {
  // 64 KiB of code and input, and 16 arguments of 1 KiB.
  const fields = ["apl-dyalog", "", "x".repeat(32 * 1024), "", "y".repeat(32 * 1024),
    ...Array.from({ length: 16 }, (_, i) => String(i % 10).repeat(1024))];

  const parsed = codec.parsePermalink(codec.fieldsToPermalink(fields).slice(1));

  assert.deepEqual(parsed, { language: "apl-dyalog", fields });
});

test("a permalink whose fields come to just under 1 MiB opens", () => {
  const fields = permalinkFields("x".repeat((1 << 20) - 100), []);

  assert.deepEqual(codec.parsePermalink(codec.fieldsToPermalink(fields).slice(1)).fields, fields);
});

test("a permalink whose fields inflate past 1 MiB is refused, and decoding stops soon after", () => {
  const fragment = codec.fieldsToPermalink(permalinkFields("x".repeat(16 << 20), [])).slice(1);
  const real = globalThis.pako;
  let inflated = 0;
  // Counts the bytes pako inflates, whichever of its interfaces the codec uses.
  class CountingInflate extends real.Inflate {
    push(data, mode) {
      try {
        return super.push(data, mode);
      } finally {
        inflated = Math.max(inflated, this.strm.total_out);
      }
    }
  }
  globalThis.pako = {
    ...real,
    Inflate: CountingInflate,
    inflateRaw(data, options) {
      const result = real.inflateRaw(data, options);
      inflated = result.length;
      return result;
    },
  };
  try {
    assert.throws(() => codec.parsePermalink(fragment), { name: "RangeError", message: /more than 1 MiB/ });
  } finally {
    globalThis.pako = real;
  }
  assert.ok(inflated <= (1 << 20) + (64 << 10), `inflated ${inflated} bytes of a 16 MiB permalink`);
});

test("a permalink with 256 arguments opens and one with 257 is refused", () => {
  const args = (n) => Array.from({ length: n }, () => "a");

  assert.equal(codec.parsePermalink(codec.fieldsToPermalink(permalinkFields("⎕←1", args(256))).slice(1)).fields.length, 5 + 256);
  assert.throws(() => codec.parsePermalink(codec.fieldsToPermalink(permalinkFields("⎕←1", args(257))).slice(1)),
    { name: "RangeError", message: /more than 256 arguments/ });
});
