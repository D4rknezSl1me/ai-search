// Unit tests for the proxy pool. No browser is launched: the pool is pure
// bookkeeping over a proxy list + per-host pinning + health, so we assert the
// invariants that make it a coherent egress layer — parsing, per-host pinning,
// load spreading, cooldown on failure, reassign-away-from-a-dead-proxy, and the
// all-benched fallback.

import assert from "node:assert/strict";
import test from "node:test";
import { ProxyPool, parseProxies, parseProxyLine } from "./proxies.js";

test("parseProxyLine splits scheme/host/port and pulls credentials out of server", () => {
  const e = parseProxyLine("http://user:pass@10.0.0.1:8080");
  assert.ok(e);
  assert.equal(e.server, "http://10.0.0.1:8080");
  assert.equal(e.username, "user");
  assert.equal(e.password, "pass");
  assert.equal(e.label, "http://10.0.0.1:8080"); // no creds leak into the label

  // Bare host:port defaults to http; socks5 scheme is preserved.
  assert.equal(parseProxyLine("127.0.0.1:1080")?.server, "http://127.0.0.1:1080");
  assert.equal(parseProxyLine("socks5://127.0.0.1:1080")?.server, "socks5://127.0.0.1:1080");

  // Junk / comments / no port → dropped, not crashed.
  assert.equal(parseProxyLine("# a comment"), null);
  assert.equal(parseProxyLine("not a proxy"), null);
  assert.equal(parseProxyLine("http://noport.example"), null);
});

test("parseProxies handles newline + comma lists, comments, and dedupes", () => {
  const raw = "http://a:1\n# skip me\nhttp://b:2, http://a:1\n\nsocks5://c:3";
  const entries = parseProxies(raw);
  assert.deepEqual(
    entries.map((e) => e.server),
    ["http://a:1", "http://b:2", "socks5://c:3"], // a:1 appears once
  );
});

test("an empty pool is disabled and hands back no lease (direct connection)", () => {
  const pool = new ProxyPool({ entries: [], cooldownMs: 10, maxCooldownMs: 100 });
  assert.equal(pool.enabled, false);
  assert.equal(pool.size, 0);
  assert.equal(pool.acquire("example.com"), null);
});

test("a host is pinned to one proxy across visits (stable egress IP)", () => {
  const pool = new ProxyPool({
    entries: parseProxies("http://a:1\nhttp://b:2"),
    cooldownMs: 10,
    maxCooldownMs: 100,
  });
  const first = pool.acquire("site.test");
  const second = pool.acquire("site.test");
  assert.ok(first && second);
  assert.equal(first.label, second.label); // same proxy on the return visit
});

test("new hosts spread across proxies by least-loaded selection", () => {
  const pool = new ProxyPool({
    entries: parseProxies("http://a:1\nhttp://b:2"),
    cooldownMs: 10,
    maxCooldownMs: 100,
  });
  const one = pool.acquire("one.test")!;
  const two = pool.acquire("two.test")!;
  // Two hosts, two proxies, empty to start → they must land on different proxies.
  assert.notEqual(one.label, two.label);
});

test("a failing proxy enters cooldown and drops out of the healthy count", () => {
  let clock = 1_000;
  const pool = new ProxyPool({
    entries: parseProxies("http://a:1\nhttp://b:2"),
    cooldownMs: 100,
    maxCooldownMs: 1000,
    now: () => clock,
    onWarn: () => {},
  });
  assert.equal(pool.healthyCount(), 2);
  const lease = pool.acquire("bad.test")!;
  lease.report(false); // one failure → benched for cooldownMs
  assert.equal(pool.healthyCount(), 1);
  // Recovers once the cooldown elapses.
  clock += 100;
  assert.equal(pool.healthyCount(), 2);
});

test("a host is reassigned off a proxy that has gone into cooldown", () => {
  let clock = 1_000;
  const pool = new ProxyPool({
    entries: parseProxies("http://a:1\nhttp://b:2"),
    cooldownMs: 100,
    maxCooldownMs: 1000,
    now: () => clock,
    onWarn: () => {},
  });
  const first = pool.acquire("host.test")!;
  const pinned = first.label;
  first.report(false); // pinned proxy is now benched

  const second = pool.acquire("host.test")!;
  assert.notEqual(second.label, pinned); // moved to the still-healthy proxy
});

test("cooldown backs off exponentially and is capped", () => {
  let clock = 0;
  const pool = new ProxyPool({
    entries: parseProxies("http://a:1"),
    cooldownMs: 100,
    maxCooldownMs: 250,
    now: () => clock,
    onWarn: () => {},
  });
  const l1 = pool.acquire("h.test")!;
  l1.report(false); // failure #1 → 100ms benched
  assert.equal(pool.healthyCount(), 0);
  clock = 100;
  assert.equal(pool.healthyCount(), 1); // recovered exactly at 100ms

  const l2 = pool.acquire("h.test")!;
  l2.report(false); // failure #2 → 200ms
  clock = 100 + 199;
  assert.equal(pool.healthyCount(), 0);
  clock = 100 + 200;
  assert.equal(pool.healthyCount(), 1);

  const l3 = pool.acquire("h.test")!;
  l3.report(false); // failure #3 would be 400ms, capped to 250ms
  clock = 300 + 250;
  assert.equal(pool.healthyCount(), 1);
});

test("a success clears the failure streak and cooldown", () => {
  let clock = 0;
  const pool = new ProxyPool({
    entries: parseProxies("http://a:1"),
    cooldownMs: 100,
    maxCooldownMs: 1000,
    now: () => clock,
    onWarn: () => {},
  });
  const l1 = pool.acquire("h.test")!;
  l1.report(false);
  clock = 100;
  const l2 = pool.acquire("h.test")!;
  l2.report(true); // healthy again, streak reset
  const l3 = pool.acquire("h.test")!;
  l3.report(false); // this is failure #1 again → only 100ms, not 200ms
  clock = 100 + 100;
  assert.equal(pool.healthyCount(), 1);
});

test("when every proxy is benched, acquire still returns one (recall-first)", () => {
  const clock = 1_000;
  const pool = new ProxyPool({
    entries: parseProxies("http://a:1\nhttp://b:2"),
    cooldownMs: 100,
    maxCooldownMs: 1000,
    now: () => clock,
    onWarn: () => {},
  });
  pool.acquire("x.test")!.report(false);
  pool.acquire("y.test")!.report(false);
  assert.equal(pool.healthyCount(), 0);
  // A brand-new host still gets a lease rather than null — better to try a
  // cooling proxy than to fail the render outright.
  const lease = pool.acquire("z.test");
  assert.ok(lease);
  assert.ok(lease.option.server.startsWith("http://"));
});
