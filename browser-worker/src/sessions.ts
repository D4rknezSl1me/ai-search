// Session / cookie persistence for the render pool (docs/04 §5, §6 "Session/cookie
// reuse"). A fresh isolated context per job is good for *isolation* but bad for
// *coherence*: presenting a brand-new fingerprint AND an empty cookie jar to the
// same host on every visit is itself a bot tell, and it throws away any cookies a
// site set to remember the "browser". This store keeps, per host, ONE pinned
// identity plus its accumulated Playwright storageState (cookies + localStorage),
// so a returning visit resumes a warm, self-consistent session. Different hosts
// stay fully isolated (keyed by host) — sessions never bleed between targets.
//
// The state is persisted to disk (a mounted volume) so warm sessions survive a
// worker restart, and it is the substrate authenticated targets need: an injected
// login cookie for a host lands in exactly this jar and is reused thereafter.
//
// Concurrency: renders of the *same* host are serialized through a per-key lock so
// two contexts can't race on one cookie jar (also more human — one session, one
// stream of activity); different hosts render in parallel as before.

import { createHash } from "node:crypto";
import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { buildIdentity, type Identity, type Rand } from "./fingerprint.js";

// Playwright's storageState() shape — kept structural so we don't import browser
// types into this pure-ish module (it is unit-tested without launching Chromium).
export interface StorageState {
  cookies: unknown[];
  origins: unknown[];
}

const EMPTY_STATE: StorageState = { cookies: [], origins: [] };

interface SessionRecord {
  key: string;
  identity: Identity;
  storageState: StorageState;
  createdAt: number;
  lastUsedAt: number;
  uses: number;
}

/** Outcome of resolving a session, so the caller can drive metrics. */
export type SessionOrigin = "created" | "resumed" | "rotated";

/**
 * A leased session. The caller renders with `identity` + `storageState`, then
 * MUST call `release(state?)` exactly once — passing the post-render storageState
 * to persist the updated cookie jar, or nothing to drop this visit's changes.
 * Releasing unlocks the host so the next render of it can proceed.
 */
export interface SessionHandle {
  key: string;
  identity: Identity;
  storageState: StorageState;
  origin: SessionOrigin;
  release(nextState?: StorageState): Promise<void>;
}

export interface SessionStoreOptions {
  /** Directory for persisted session files. Empty string ⇒ memory-only. */
  dir: string;
  /** Rotate (new identity + empty jar) once a session is older than this (ms). */
  ttlMs: number;
  /** Injectable RNG for the pinned identity (tests). */
  rand?: Rand;
  /** Sink for non-fatal disk errors; defaults to console.warn JSON line. */
  onWarn?: (msg: string, extra: Record<string, unknown>) => void;
}

export class SessionStore {
  private readonly cache = new Map<string, SessionRecord>();
  // Tail of the per-key lock chain. Awaiting the current tail = waiting your turn.
  private readonly locks = new Map<string, Promise<void>>();
  private dirReady: Promise<void> | null = null;

  constructor(private readonly opts: SessionStoreOptions) {}

  /** Host is the session boundary: coherent per site, isolated across sites. */
  static keyFor(url: string): string {
    try {
      return new URL(url).hostname.toLowerCase() || "unknown";
    } catch {
      return "unknown";
    }
  }

  /**
   * Lease the session for `url`'s host. Blocks until any in-flight render of the
   * same host releases, then returns the pinned identity + current cookie jar.
   */
  async acquire(url: string): Promise<SessionHandle> {
    const key = SessionStore.keyFor(url);

    // Take the lock: chain a fresh gate after the previous holder's release.
    const prev = this.locks.get(key) ?? Promise.resolve();
    let unlock!: () => void;
    const gate = new Promise<void>((resolve) => {
      unlock = resolve;
    });
    const tail = prev.then(() => gate);
    this.locks.set(key, tail);
    await prev;

    const now = Date.now();
    let record = this.cache.get(key) ?? (await this.loadFromDisk(key));
    let origin: SessionOrigin;
    if (!record) {
      record = this.fresh(key, now);
      origin = "created";
    } else if (this.opts.ttlMs > 0 && now - record.createdAt > this.opts.ttlMs) {
      // Aged out — rotate to a new identity so no cookie jar / fingerprint pair
      // lives forever, which would itself become a stable tracking signal.
      record = this.fresh(key, now);
      origin = "rotated";
    } else {
      origin = "resumed";
    }
    this.cache.set(key, record);

    let released = false;
    const release = async (nextState?: StorageState): Promise<void> => {
      if (released) return;
      released = true;
      if (nextState) {
        record!.storageState = nextState;
        record!.lastUsedAt = Date.now();
        record!.uses += 1;
        await this.persist(record!);
      }
      // Drop the lock tail if no one has queued behind us, then open the gate.
      if (this.locks.get(key) === tail) this.locks.delete(key);
      unlock();
    };

    return { key, identity: record.identity, storageState: record.storageState, origin, release };
  }

  private fresh(key: string, now: number): SessionRecord {
    return {
      key,
      identity: buildIdentity(this.opts.rand ?? Math.random),
      storageState: { cookies: [], origins: [] },
      createdAt: now,
      lastUsedAt: now,
      uses: 0,
    };
  }

  // --- persistence -------------------------------------------------------

  private fileFor(key: string): string {
    // Hash keeps the filename filesystem-safe and collision-free across odd hosts.
    const hash = createHash("sha1").update(key).digest("hex").slice(0, 16);
    return join(this.opts.dir, `${hash}.json`);
  }

  private async ensureDir(): Promise<void> {
    if (!this.dirReady) this.dirReady = mkdir(this.opts.dir, { recursive: true }).then(() => {});
    return this.dirReady;
  }

  private async loadFromDisk(key: string): Promise<SessionRecord | null> {
    if (!this.opts.dir) return null;
    try {
      const raw = await readFile(this.fileFor(key), "utf8");
      const rec = JSON.parse(raw) as SessionRecord;
      // Guard against partially-written / schema-drifted files.
      if (!rec || rec.key !== key || !rec.identity || !rec.storageState) return null;
      if (!Array.isArray(rec.storageState.cookies)) rec.storageState = { ...EMPTY_STATE };
      return rec;
    } catch {
      return null; // absent or unreadable → treat as no session
    }
  }

  private async persist(record: SessionRecord): Promise<void> {
    if (!this.opts.dir) return; // memory-only mode
    try {
      await this.ensureDir();
      const path = this.fileFor(record.key);
      const tmp = `${path}.${process.pid}.tmp`;
      // Write-then-rename so a crash mid-write never leaves a half-file that a
      // future load would parse into a broken session.
      await writeFile(tmp, JSON.stringify(record), "utf8");
      await rename(tmp, path);
    } catch (err) {
      this.warn("session persist failed", { key: record.key, error: String(err) });
    }
  }

  private warn(msg: string, extra: Record<string, unknown>): void {
    if (this.opts.onWarn) return this.opts.onWarn(msg, extra);
    console.warn(JSON.stringify({ ts: new Date().toISOString(), level: "warn", msg, ...extra }));
  }
}
