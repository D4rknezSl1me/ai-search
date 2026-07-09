// Unit tests for the fingerprint identity logic. Pure — no browser launched, so
// these run under `node --test` on the compiled output. The whole point of the
// module is *internal consistency*, so that is what we assert: no derived field
// may contradict another.

import assert from "node:assert/strict";
import test from "node:test";
import {
  buildIdentity,
  contextOptions,
  stealthPayload,
  type Identity,
  type Rand,
} from "./fingerprint.js";

// A deterministic RNG cycling through fixed values so a test can steer every
// pick() and reproduce an exact identity.
function seq(values: number[]): Rand {
  let i = 0;
  return () => values[i++ % values.length];
}

test("identity is internally consistent across 500 random draws", () => {
  for (let n = 0; n < 500; n++) {
    const id = buildIdentity();

    // UA advertises the same Chrome major as the client hints.
    assert.match(id.userAgent, new RegExp(`Chrome/${id.chromeMajor}\\.0\\.0\\.0`));
    assert.ok(
      id.secChUa.includes(`"Chromium";v="${id.chromeMajor}"`),
      `secChUa "${id.secChUa}" missing Chromium v${id.chromeMajor}`,
    );
    assert.ok(id.secChUa.includes(`"Google Chrome";v="${id.chromeMajor}"`));

    // navigator.platform must agree with the UA platform token and CH-Platform.
    assertPlatformCoherent(id);

    // languages[0] is the locale; a bare-language fallback follows.
    assert.equal(id.languages[0], id.locale);
    assert.ok(id.acceptLanguage.startsWith(id.locale));

    // Region/timezone pairing: US locale → America/*, GB → Europe/London.
    if (id.locale === "en-US") assert.match(id.timezoneId, /^America\//);
    if (id.locale === "en-GB") assert.equal(id.timezoneId, "Europe/London");

    // WebGL must not read as a software rasterizer (the headless tell).
    assert.doesNotMatch(id.webglRenderer, /swiftshader|llvmpipe|software/i);
  }
});

function assertPlatformCoherent(id: Identity): void {
  const table: Record<string, { uaToken: RegExp; chPlatform: string }> = {
    Win32: { uaToken: /Windows NT/, chPlatform: '"Windows"' },
    MacIntel: { uaToken: /Mac OS X/, chPlatform: '"macOS"' },
    "Linux x86_64": { uaToken: /Linux x86_64/, chPlatform: '"Linux"' },
  };
  const want = table[id.platform];
  assert.ok(want, `unexpected platform ${id.platform}`);
  assert.match(id.userAgent, want.uaToken);
  assert.equal(id.uaPlatform, want.chPlatform);
}

test("contextOptions client hints match the identity", () => {
  const id = buildIdentity(seq([0, 0, 0, 0, 0, 0, 0, 0, 0]));
  const opts = contextOptions(id);
  assert.equal(opts.userAgent, id.userAgent);
  assert.equal(opts.locale, id.locale);
  assert.equal(opts.timezoneId, id.timezoneId);
  assert.equal(opts.extraHTTPHeaders["sec-ch-ua"], id.secChUa);
  assert.equal(opts.extraHTTPHeaders["sec-ch-ua-platform"], id.uaPlatform);
  assert.equal(opts.extraHTTPHeaders["sec-ch-ua-mobile"], "?0");
  assert.equal(opts.extraHTTPHeaders["accept-language"], id.acceptLanguage);
});

test("stealthPayload drops the GREASE brand from userAgentData brands input", () => {
  const id = buildIdentity();
  const p = stealthPayload(id);
  // The payload carries the full brand list (incl. the greasy Not-A-Brand);
  // stealthInit is what filters it. Confirm the real brands survived parsing.
  const chromium = p.brands.find((b) => b.brand === "Chromium");
  assert.ok(chromium, "Chromium brand parsed from secChUa");
  assert.equal(chromium.version, String(id.chromeMajor));
  assert.equal(p.chPlatform, id.uaPlatform.replace(/"/g, ""));
  assert.ok(p.brands.some((b) => /not.?a.?brand/i.test(b.brand)), "GREASE brand present");
});

test("the injected RNG makes identity fully deterministic", () => {
  const a = buildIdentity(seq([0.99, 0.99, 0.99, 0.99, 0.99, 0.99, 0.99, 0.99]));
  const b = buildIdentity(seq([0.99, 0.99, 0.99, 0.99, 0.99, 0.99, 0.99, 0.99]));
  assert.deepEqual(a, b);
});
