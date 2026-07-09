// Unit tests for the session store. No browser is launched: the store is pure
// bookkeeping over identity + storageState, so we assert the invariants that make
// it a *coherent* session layer — per-host keying, identity pinning across
// resumes, cookie-jar round-tripping to disk, per-key serialization, and TTL
// rotation.

import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { SessionStore, type StorageState } from "./sessions.js";

function jarWith(name: string): StorageState {
  return { cookies: [{ name, value: "1", domain: "x", path: "/" }], origins: [] };
}

async function withTmpDir(fn: (dir: string) => Promise<void>): Promise<void> {
  const dir = await mkdtemp(join(tmpdir(), "bw-sessions-"));
  try {
    await fn(dir);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

test("keyFor collapses to the host and lowercases it", () => {
  assert.equal(SessionStore.keyFor("https://Example.COM/a?b=c#d"), "example.com");
  assert.equal(SessionStore.keyFor("http://example.com:8080/x"), "example.com");
  assert.equal(SessionStore.keyFor("not a url"), "unknown");
});

test("a first visit is 'created' with an empty jar", async () => {
  const store = new SessionStore({ dir: "", ttlMs: 0 });
  const h = await store.acquire("https://site.test/page");
  assert.equal(h.origin, "created");
  assert.deepEqual(h.storageState, { cookies: [], origins: [] });
  await h.release();
});

test("a return visit resumes the same identity and persisted jar", async () => {
  const store = new SessionStore({ dir: "", ttlMs: 0 });
  const first = await store.acquire("https://site.test/a");
  const identity = first.identity;
  await first.release(jarWith("sid"));

  const second = await store.acquire("https://site.test/b"); // same host
  assert.equal(second.origin, "resumed");
  // Identity is pinned — presenting a new fingerprint to a warm cookie jar is a tell.
  assert.deepEqual(second.identity, identity);
  assert.equal((second.storageState.cookies[0] as { name: string }).name, "sid");
  await second.release();
});

test("different hosts get independent, isolated sessions", async () => {
  const store = new SessionStore({ dir: "", ttlMs: 0 });
  const a = await store.acquire("https://a.test/");
  await a.release(jarWith("acookie"));
  const b = await store.acquire("https://b.test/");
  assert.equal(b.origin, "created");
  assert.deepEqual(b.storageState.cookies, []);
  await b.release();
});

test("state round-trips through disk (survives a fresh store)", async () => {
  await withTmpDir(async (dir) => {
    const store1 = new SessionStore({ dir, ttlMs: 0 });
    const h1 = await store1.acquire("https://persist.test/");
    const pinned = h1.identity;
    await h1.release(jarWith("token"));

    // A brand-new store with a cold cache must recover the session from disk.
    const store2 = new SessionStore({ dir, ttlMs: 0 });
    const h2 = await store2.acquire("https://persist.test/");
    assert.equal(h2.origin, "resumed");
    assert.deepEqual(h2.identity, pinned);
    assert.equal((h2.storageState.cookies[0] as { name: string }).name, "token");
    await h2.release();
  });
});

test("acquire serializes same-host renders (no cookie-jar race)", async () => {
  const store = new SessionStore({ dir: "", ttlMs: 0 });
  const order: string[] = [];

  const first = await store.acquire("https://serial.test/");
  order.push("first-acquired");

  // Second acquire for the same host must block until we release the first.
  let secondAcquired = false;
  const secondP = store.acquire("https://serial.test/").then((h) => {
    secondAcquired = true;
    order.push("second-acquired");
    return h;
  });

  // Give the microtask queue a chance; it must still be blocked.
  await new Promise((r) => setTimeout(r, 10));
  assert.equal(secondAcquired, false, "second acquire ran before first released");

  order.push("first-released");
  await first.release();
  const second = await secondP;
  assert.deepEqual(order, ["first-acquired", "first-released", "second-acquired"]);
  await second.release();
});

test("a session past its TTL rotates to a new identity and empty jar", async () => {
  const store = new SessionStore({ dir: "", ttlMs: 1 });
  const first = await store.acquire("https://ttl.test/");
  const oldIdentity = first.identity;
  await first.release(jarWith("stale"));

  await new Promise((r) => setTimeout(r, 5)); // exceed the 1ms TTL

  const second = await store.acquire("https://ttl.test/");
  assert.equal(second.origin, "rotated");
  // Rotation clears the jar; the pinned identity is re-minted (may or may not
  // land on the same random values, so we assert the definitive signal: origin
  // + a cleared cookie jar).
  assert.deepEqual(second.storageState.cookies, []);
  void oldIdentity;
  await second.release();
});
