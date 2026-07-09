// Fingerprint hardening for the render pool (docs/04 §5 anti-detection).
//
// The renderer rotates a fresh identity per job, but a *rotated* identity is
// only useful if it is internally *consistent*. The classic headless tells are
// mismatches, not any single value: a UA that says "Chrome/126" while the
// browser sends no `Sec-CH-UA` client hints, `navigator.platform` that disagrees
// with the UA platform token, an empty `navigator.plugins`, a missing
// `window.chrome`, or a WebGL renderer that reads "SwiftShader"/"llvmpipe"
// (the software rasterizer headless Chromium falls back to).
//
// This module builds ONE coherent identity — OS profile, region (locale +
// timezone + Accept-Language), Chrome major version, hardware — and derives the
// UA string, the matching client-hint headers, and a stealth init-script from
// that single source of truth. It deliberately imports nothing from Playwright
// so the identity logic stays pure and unit-testable without launching a browser.

/** A self-consistent browser identity used for a single render. */
export interface Identity {
  viewport: { width: number; height: number };
  locale: string;
  /** navigator.languages, primary first (e.g. ["en-GB", "en"]). */
  languages: string[];
  /** Accept-Language header value matching `languages`. */
  acceptLanguage: string;
  timezoneId: string;
  /** Chrome major version, shared by the UA string and the client hints. */
  chromeMajor: number;
  userAgent: string;
  /** navigator.platform (e.g. "Win32", "MacIntel", "Linux x86_64"). */
  platform: string;
  /** Sec-CH-UA-Platform value, quoted (e.g. "\"Windows\""). */
  uaPlatform: string;
  /** Sec-CH-UA brand list, e.g. '"Chromium";v="126", ...'. */
  secChUa: string;
  hardwareConcurrency: number;
  deviceMemory: number;
  /** Spoofed WebGL UNMASKED_VENDOR_WEBGL / UNMASKED_RENDERER_WEBGL. */
  webglVendor: string;
  webglRenderer: string;
}

interface OsProfile {
  /** UA platform token, e.g. "Windows NT 10.0; Win64; x64". */
  uaToken: string;
  /** navigator.platform. */
  platform: string;
  /** Sec-CH-UA-Platform (unquoted brand). */
  chPlatform: string;
  /** Plausible GPU strings so WebGL doesn't read as a software rasterizer. */
  gpus: Array<{ vendor: string; renderer: string }>;
}

const OS_PROFILES: OsProfile[] = [
  {
    uaToken: "Windows NT 10.0; Win64; x64",
    platform: "Win32",
    chPlatform: "Windows",
    gpus: [
      {
        vendor: "Google Inc. (NVIDIA)",
        renderer:
          "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 Direct3D11 vs_5_0 ps_5_0, D3D11)",
      },
      {
        vendor: "Google Inc. (Intel)",
        renderer:
          "ANGLE (Intel, Intel(R) UHD Graphics 630 Direct3D11 vs_5_0 ps_5_0, D3D11)",
      },
    ],
  },
  {
    uaToken: "Macintosh; Intel Mac OS X 10_15_7",
    platform: "MacIntel",
    chPlatform: "macOS",
    gpus: [
      { vendor: "Google Inc. (Apple)", renderer: "ANGLE (Apple, Apple M1, OpenGL 4.1)" },
      {
        vendor: "Google Inc. (Intel)",
        renderer: "ANGLE (Intel, Intel(R) Iris(TM) Plus Graphics OpenGL 4.1)",
      },
    ],
  },
  {
    uaToken: "X11; Linux x86_64",
    platform: "Linux x86_64",
    chPlatform: "Linux",
    gpus: [
      {
        vendor: "Google Inc. (NVIDIA)",
        renderer: "ANGLE (NVIDIA, NVIDIA GeForce GTX 1650 OpenGL 4.6.0, OpenGL 4.6.0)",
      },
    ],
  },
];

interface Region {
  locale: string;
  languages: string[];
  acceptLanguage: string;
  timezones: string[];
}

const REGIONS: Region[] = [
  {
    locale: "en-US",
    languages: ["en-US", "en"],
    acceptLanguage: "en-US,en;q=0.9",
    timezones: ["America/New_York", "America/Chicago", "America/Los_Angeles"],
  },
  {
    locale: "en-GB",
    languages: ["en-GB", "en"],
    acceptLanguage: "en-GB,en;q=0.9",
    timezones: ["Europe/London"],
  },
];

const VIEWPORTS = [
  { width: 1366, height: 768 },
  { width: 1440, height: 900 },
  { width: 1536, height: 864 },
  { width: 1920, height: 1080 },
];

// Chrome major versions plus each version's real "GREASE" brand — the third,
// intentionally-varying token Chromium injects into Sec-CH-UA. Pairing them
// keeps the brand list identical to what a genuine build of that version emits.
const CHROME_BUILDS: Array<{ major: number; greaseBrand: string }> = [
  { major: 124, greaseBrand: '"Not-A.Brand";v="99"' },
  { major: 125, greaseBrand: '"Not.A/Brand";v="24"' },
  { major: 126, greaseBrand: '"Not/A)Brand";v="24"' },
];

const HARDWARE_CONCURRENCY = [4, 8, 12, 16];
const DEVICE_MEMORY = [8, 16];

/** Injectable RNG so tests can drive coverage deterministically. */
export type Rand = () => number;

function pick<T>(arr: T[], rand: Rand): T {
  return arr[Math.floor(rand() * arr.length)];
}

/**
 * Build one internally-consistent identity. Every derived string traces back to
 * the same OS/region/version choices, so no field contradicts another.
 */
export function buildIdentity(rand: Rand = Math.random): Identity {
  const os = pick(OS_PROFILES, rand);
  const region = pick(REGIONS, rand);
  const build = pick(CHROME_BUILDS, rand);
  const gpu = pick(os.gpus, rand);

  const userAgent =
    `Mozilla/5.0 (${os.uaToken}) AppleWebKit/537.36 ` +
    `(KHTML, like Gecko) Chrome/${build.major}.0.0.0 Safari/537.36`;

  const secChUa =
    `"Chromium";v="${build.major}", ` +
    `"Google Chrome";v="${build.major}", ` +
    `${build.greaseBrand}`;

  return {
    viewport: pick(VIEWPORTS, rand),
    locale: region.locale,
    languages: region.languages,
    acceptLanguage: region.acceptLanguage,
    timezoneId: pick(region.timezones, rand),
    chromeMajor: build.major,
    userAgent,
    platform: os.platform,
    uaPlatform: `"${os.chPlatform}"`,
    secChUa,
    hardwareConcurrency: pick(HARDWARE_CONCURRENCY, rand),
    deviceMemory: pick(DEVICE_MEMORY, rand),
    webglVendor: gpu.vendor,
    webglRenderer: gpu.renderer,
  };
}

/** Playwright `newContext` options derived from an identity. */
export interface ContextOptions {
  viewport: { width: number; height: number };
  locale: string;
  timezoneId: string;
  userAgent: string;
  extraHTTPHeaders: Record<string, string>;
}

/**
 * Map an identity to Playwright context options. The client-hint headers are set
 * explicitly because Chromium otherwise emits the *real* build's Sec-CH-UA even
 * when the UA string is overridden — the exact mismatch this module exists to
 * prevent.
 */
export function contextOptions(id: Identity): ContextOptions {
  return {
    viewport: id.viewport,
    locale: id.locale,
    timezoneId: id.timezoneId,
    userAgent: id.userAgent,
    extraHTTPHeaders: {
      "accept-language": id.acceptLanguage,
      "sec-ch-ua": id.secChUa,
      "sec-ch-ua-mobile": "?0",
      "sec-ch-ua-platform": id.uaPlatform,
    },
  };
}

// Serializable payload handed to the page-context init script. Kept flat/JSON so
// Playwright can marshal it across the boundary.
export interface StealthPayload {
  languages: string[];
  platform: string;
  chPlatform: string;
  brands: Array<{ brand: string; version: string }>;
  chromeMajor: number;
  hardwareConcurrency: number;
  deviceMemory: number;
  webglVendor: string;
  webglRenderer: string;
}

/** Parse a Sec-CH-UA string back into userAgentData brand entries. */
function brandsFromSecChUa(secChUa: string): Array<{ brand: string; version: string }> {
  const out: Array<{ brand: string; version: string }> = [];
  const re = /"([^"]+)";v="([^"]+)"/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(secChUa)) !== null) {
    out.push({ brand: m[1], version: m[2] });
  }
  return out;
}

export function stealthPayload(id: Identity): StealthPayload {
  return {
    languages: id.languages,
    platform: id.platform,
    chPlatform: id.uaPlatform.replace(/"/g, ""),
    brands: brandsFromSecChUa(id.secChUa),
    chromeMajor: id.chromeMajor,
    hardwareConcurrency: id.hardwareConcurrency,
    deviceMemory: id.deviceMemory,
    webglVendor: id.webglVendor,
    webglRenderer: id.webglRenderer,
  };
}

/**
 * Init script (runs in page context before any site code) that patches the
 * remaining headless tells to agree with `p`. Written as a standalone function
 * so Playwright can serialize it; `p` is the marshalled StealthPayload.
 */
export function stealthInit(p: StealthPayload): void {
  // navigator.webdriver — the canonical automation flag.
  Object.defineProperty(navigator, "webdriver", { get: () => false });

  // Language + platform coherence with the UA / client hints.
  Object.defineProperty(navigator, "languages", { get: () => p.languages });
  Object.defineProperty(navigator, "platform", { get: () => p.platform });
  Object.defineProperty(navigator, "hardwareConcurrency", {
    get: () => p.hardwareConcurrency,
  });
  Object.defineProperty(navigator, "deviceMemory", { get: () => p.deviceMemory });

  // navigator.userAgentData — present on real Chrome, absent on many stealth
  // shims. Mirror the Sec-CH-UA brands and answer high-entropy queries.
  const uaData = {
    brands: p.brands.filter((b) => !/not.?a.?brand/i.test(b.brand)),
    mobile: false,
    platform: p.chPlatform,
    getHighEntropyValues(hints: string[]): Promise<Record<string, unknown>> {
      const full: Record<string, unknown> = {
        architecture: "x86",
        bitness: "64",
        brands: p.brands,
        fullVersionList: p.brands.map((b) => ({
          brand: b.brand,
          version: `${p.chromeMajor}.0.0.0`,
        })),
        mobile: false,
        model: "",
        platform: p.chPlatform,
        platformVersion: "10.0.0",
        uaFullVersion: `${p.chromeMajor}.0.0.0`,
        wow64: false,
      };
      const out: Record<string, unknown> = {};
      for (const h of hints) if (h in full) out[h] = full[h];
      return Promise.resolve(out);
    },
    toJSON(): Record<string, unknown> {
      return { brands: this.brands, mobile: this.mobile, platform: this.platform };
    },
  };
  Object.defineProperty(navigator, "userAgentData", { get: () => uaData });

  // window.chrome — a bare object is enough to clear the "is this Chrome?" check.
  if (!("chrome" in window)) {
    Object.defineProperty(window, "chrome", {
      writable: true,
      enumerable: true,
      configurable: false,
      value: { runtime: {} },
    });
  }

  // Non-empty plugins/mimeTypes. Headless ships an empty PluginArray.
  const fakePlugins = [
    { name: "PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" },
    { name: "Chrome PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" },
    { name: "Chromium PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" },
  ];
  Object.defineProperty(navigator, "plugins", {
    get: () => fakePlugins.slice() as unknown as PluginArray,
  });

  // WebGL vendor/renderer — mask the software rasterizer with the identity's GPU.
  const patchGetParameter = (proto: { getParameter?: (n: number) => unknown } | undefined) => {
    if (!proto || typeof proto.getParameter !== "function") return;
    const original = proto.getParameter;
    proto.getParameter = function (this: unknown, param: number): unknown {
      // UNMASKED_VENDOR_WEBGL / UNMASKED_RENDERER_WEBGL from WEBGL_debug_renderer_info.
      if (param === 37445) return p.webglVendor;
      if (param === 37446) return p.webglRenderer;
      return original.call(this, param);
    };
  };
  patchGetParameter(
    (globalThis as { WebGLRenderingContext?: { prototype: unknown } }).WebGLRenderingContext
      ?.prototype as { getParameter?: (n: number) => unknown } | undefined,
  );
  patchGetParameter(
    (globalThis as { WebGL2RenderingContext?: { prototype: unknown } }).WebGL2RenderingContext
      ?.prototype as { getParameter?: (n: number) => unknown } | undefined,
  );

  // permissions.query for "notifications" returns "denied" under headless while
  // Notification.permission reads "default" — a well-known contradiction.
  const perms = navigator.permissions;
  if (perms && typeof perms.query === "function") {
    const originalQuery = perms.query.bind(perms);
    perms.query = (desc: PermissionDescriptor): Promise<PermissionStatus> => {
      if (desc && desc.name === "notifications") {
        return Promise.resolve({ state: "prompt" } as PermissionStatus);
      }
      return originalQuery(desc);
    };
  }
}
