"""Candidate extraction for entity discovery (docs/15-DISCOVERY-AGENT.md §4.2).

Turn a fetched/retrieved document into `Candidate`s the resolver can score:
find name mentions anchored on what the brief knows, and pull the nearby signals
(attribute values, co-mentioned names, a handle) that let `entity_resolve` decide
whether it's the target. Deterministic and dependency-free — the LLM-assisted
"read" is a later refinement layered on top; this heuristic pass is the always-on
backbone (recall-first: it never fails, and over-emits rather than under-emits,
since the resolver + confidence floor filter downstream).

Pure (stdlib `re`); reuses the resolver's accent-tolerant normalizer.
"""

from __future__ import annotations

import re
from urllib.parse import urlsplit

from app.entity_brief import TargetBrief
from app.entity_resolve import Candidate, _norm, _tokens

_AGE_KEYS = ("birth_year", "approx_age")
_WINDOW = 220                 # chars of context scanned around a name mention

# A Capitalized word run (2–4 tokens): "Giulia Rossi", "G. Rossi", "Liceo Volta".
# Latin-1 uppercase incl. accents, skipping × (0xD7). The inter-token separator is
# horizontal whitespace only ([^\S\r\n]) so a "name" never bridges a line break —
# otherwise a heading like "Ada Lovelace\nThe Right Honourable…" is captured as one
# bogus name (caught in live verification).
_CAP = r"[A-ZÀ-ÖØ-Þ]"
_NAME_SEQ = re.compile(rf"{_CAP}[\w'’.-]*(?:[^\S\r\n]+{_CAP}[\w'’.-]*){{0,3}}", re.UNICODE)
_HANDLE = re.compile(r"@([A-Za-z0-9_.]{2,30})")
_YEAR = re.compile(r"\b(19\d{2}|20\d{2})\b")

# Social hosts whose first path segment is the profile handle.
_PROFILE_HOSTS = {
    "instagram.com", "tiktok.com", "twitter.com", "x.com", "facebook.com",
    "github.com", "t.me", "vk.com", "reddit.com",
}


def _handle_from_url(url: str) -> str:
    """A profile URL's handle, e.g. instagram.com/giulia.rossi → @giulia.rossi."""
    if not url:
        return ""
    try:
        parts = urlsplit(url)
    except ValueError:
        return ""
    host = parts.netloc.lower().removeprefix("www.")
    if host not in _PROFILE_HOSTS:
        return ""
    seg = parts.path.strip("/").split("/", 1)[0]
    seg = seg.removeprefix("@").lstrip("u/")  # reddit /u/name, tiktok /@name
    if seg and re.fullmatch(r"[A-Za-z0-9_.]{2,30}", seg):
        return f"@{seg}"
    return ""


def _names(text: str) -> list[tuple[str, int, int]]:
    return [(m.group(), m.start(), m.end()) for m in _NAME_SEQ.finditer(text)]


def _co_mentions(window: str, exclude: str) -> list[str]:
    """Other capitalized name-like sequences near a candidate (dedup, capped)."""
    exclude_norm = _norm(exclude)
    out: list[str] = []
    seen: set[str] = {exclude_norm}
    for nm, _, _ in _names(window):
        key = _norm(nm)
        if key and key not in seen and len(key) > 2:
            seen.add(key)
            out.append(nm)
        if len(out) >= 6:
            break
    return out


def extract_candidates(
    brief: TargetBrief,
    *,
    text: str,
    url: str = "",
    title: str = "",
    platform: str = "open_web",
    max_candidates: int = 3,
) -> list[Candidate]:
    """Extract candidate entities from one document, anchored on the brief's name.

    Anchors on the surname (or the given name if no surname is known): only name
    mentions containing that token become candidates, so a page isn't shredded
    into every capitalized phrase. For each, the surrounding window supplies the
    corroborating attributes (brief values that appear nearby), co-mentions (for
    relationship resolution), and a handle (from the URL or an @mention).
    """
    body = f"{title}\n{text}" if title else (text or "")
    if not body:
        return []

    anchor = _norm(brief.surname) or _norm(brief.given_name)
    url_handle = _handle_from_url(url)
    discriminators = brief.discriminators()

    out: list[Candidate] = []
    seen: set[str] = set()
    for nm, start, end in _names(body):
        if anchor and anchor not in set(_tokens(nm)):
            continue
        key = _norm(nm)
        if not key or key in seen:
            continue
        seen.add(key)

        window = body[max(0, start - _WINDOW) : end + _WINDOW]
        window_norm = _norm(window)

        attrs: dict[str, str] = {}
        for k, v in discriminators.items():
            if k in _AGE_KEYS:
                if _norm(v) in window_norm:            # the exact year appears
                    attrs[k] = v
                else:
                    ym = _YEAR.search(window)
                    if ym:
                        attrs[k] = ym.group(1)
            elif _norm(v) and _norm(v) in window_norm:
                attrs[k] = v

        handle = url_handle
        if not handle:
            hm = _HANDLE.search(window)
            if hm:
                handle = f"@{hm.group(1)}"

        out.append(Candidate(
            name=nm,
            handle=handle,
            attributes=attrs,
            co_mentions=_co_mentions(window, exclude=nm),
            source_url=url,
            platform=platform,
        ))
        if len(out) >= max_candidates:
            break

    # A profile page whose handle matches the surname but whose body never spells
    # the name out is still a lead (recall-first) — emit it from the URL alone.
    if not out and url_handle and anchor and anchor in url_handle.lower():
        out.append(Candidate(name="", handle=url_handle, source_url=url, platform=platform))
    return out
