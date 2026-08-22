"""Entity resolution for agentic discovery (docs/15-DISCOVERY-AGENT.md §4.2).

The crux of precision in the discovery loop: given a fetched candidate (a name +
the attributes/co-mentions found around it on a page or profile), score how well
it matches the `TargetBrief`. A candidate above the confidence floor becomes a
lead worth expanding; below it, it's *parked*, not discarded (recall-first — a
later hop may corroborate it). The score is a transparent weighted sum whose
per-signal breakdown is kept for the confidence + cited evidence trail (§4.4).

Scoring is **relative to what the brief knows**: a candidate is scored on the
fraction of the brief's *known* signals it corroborates, so a sparse brief isn't
penalized for fields it never specified. The heaviest signal is a corroborated
**relationship** ("appears alongside Marco Rossi") — the disambiguator that
collapses namesakes (§4.2).

Pure and dependency-free (stdlib `difflib`/`unicodedata` for accent-tolerant
fuzzy matching) — unit-testable offline.
"""

from __future__ import annotations

import re
import unicodedata
from dataclasses import dataclass, field
from difflib import SequenceMatcher

from app.entity_brief import TargetBrief

# --- signal weights (higher = stronger disambiguator) ------------------------
_W_RELATIONSHIP = 5.0    # heaviest: a corroborated tie to a findable anchor
_W_SURNAME = 3.0
_W_GIVEN = 3.0
_W_AGE = 1.5
_W_ATTR_DEFAULT = 2.0    # school / employer / city ...
_ATTR_WEIGHTS: dict[str, float] = {
    "school": 2.0, "employer": 2.0, "city": 2.0, "profession": 1.5,
    "region": 1.0, "country": 1.0, "language": 0.5,
}
_AGE_KEYS = ("birth_year", "approx_age")

# Fuzzy thresholds — below these a comparison contributes nothing.
_TOKEN_FUZZY = 0.85      # surname/given-name token similarity
_VALUE_FUZZY = 0.80      # attribute / relationship value similarity
_AGE_TOLERANCE = 3       # years within which an age/birth-year still corroborates

# A candidate at/above this score is treated as the target for expansion (§4.2).
DEFAULT_MATCH_THRESHOLD = 0.6

_NONWORD = re.compile(r"[^\w]+", re.UNICODE)


@dataclass
class Candidate:
    """An entity observed while crawling, to be resolved against the brief.

    Populated by extraction (NER + LLM read, §4.2); this module never fetches.
    """

    name: str = ""                                    # extracted full/partial name
    handle: str = ""                                  # discovered social handle
    attributes: dict[str, str] = field(default_factory=dict)  # observed discriminators
    co_mentions: list[str] = field(default_factory=list)      # names seen alongside
    source_url: str = ""                              # provenance for the evidence trail
    platform: str = "open_web"


@dataclass
class MatchScore:
    """A candidate's resolution result — score plus the signals that produced it."""

    score: float
    signals: dict[str, float]          # signal name → strength in [0,1] (fired signals)

    def is_match(self, threshold: float = DEFAULT_MATCH_THRESHOLD) -> bool:
        return self.score >= threshold


# --- normalization / fuzzy helpers -------------------------------------------

def _norm(s: str) -> str:
    """Casefold + strip accents + collapse non-word runs to single spaces."""
    if not s:
        return ""
    decomposed = unicodedata.normalize("NFKD", s)
    stripped = "".join(c for c in decomposed if not unicodedata.combining(c))
    return _NONWORD.sub(" ", stripped).casefold().strip()


def _tokens(s: str) -> list[str]:
    return [t for t in _norm(s).split() if t]


def _ratio(a: str, b: str) -> float:
    if not a or not b:
        return 0.0
    return SequenceMatcher(None, a, b).ratio()


def _value_match(want: str, have: str) -> float:
    """How well an observed value corroborates a wanted one, in [0,1].

    Substring either direction → full (a page saying "Liceo A. Volta di Como"
    corroborates "Liceo Volta"); otherwise a fuzzy ratio above the floor.
    """
    w, h = _norm(want), _norm(have)
    if not w or not h:
        return 0.0
    if w in h or h in w:
        return 1.0
    r = _ratio(w, h)
    return r if r >= _VALUE_FUZZY else 0.0


def _name_match(name: str, haystack_name: str, haystack_handle: str) -> float:
    """Average corroboration of the wanted name's tokens against a candidate's
    name + handle (token-exact → substring-in-handle → fuzzy)."""
    targets = _tokens(name)
    if not targets:
        return 0.0
    hay_tokens = set(_tokens(haystack_name)) | set(_tokens(haystack_handle))
    handle_norm = _norm(haystack_handle).replace(" ", "")  # handles drop spaces
    total = 0.0
    for tok in targets:
        if tok in hay_tokens:
            total += 1.0
        elif tok in handle_norm:            # "giuliarossi" contains "rossi"
            total += 0.9
        else:
            best = max((_ratio(tok, h) for h in hay_tokens), default=0.0)
            total += best if best >= _TOKEN_FUZZY else 0.0
    return total / len(targets)


def _age_match(want: str, cand_attrs: dict[str, str]) -> float:
    """Corroborate a birth_year / approx_age within tolerance, in [0,1]."""
    def _int(v: str) -> int | None:
        m = re.search(r"\d{1,4}", v or "")
        return int(m.group()) if m else None

    want_i = _int(want)
    if want_i is None:
        return 0.0
    have_i = next((_int(cand_attrs[k]) for k in _AGE_KEYS if cand_attrs.get(k)), None)
    if have_i is None:
        return 0.0
    diff = abs(want_i - have_i)
    return max(0.0, 1.0 - diff / (_AGE_TOLERANCE + 1))


# --- the scorer --------------------------------------------------------------

def score_candidate(brief: TargetBrief, candidate: Candidate) -> MatchScore:
    """Score `candidate` against `brief` in [0,1] (fraction of known signals met).

    `possible` accumulates the weight of every signal the brief actually
    specifies; `achieved` accumulates each candidate's corroboration of it. A
    brief that knows nothing scores 0 (there's nothing to resolve against).
    """
    signals: dict[str, float] = {}
    achieved = 0.0
    possible = 0.0

    def add(name: str, strength: float, weight: float) -> None:
        nonlocal achieved, possible
        possible += weight
        signals[name] = strength
        achieved += strength * weight

    if brief.surname:
        add("surname", _name_match(brief.surname, candidate.name, candidate.handle), _W_SURNAME)
    if brief.given_name:
        add("given_name", _name_match(brief.given_name, candidate.name, candidate.handle), _W_GIVEN)

    # Relationship: strongest signal — best corroboration across co-mentions.
    if brief.relationships:
        best_rel = 0.0
        for rel in brief.relationships:
            best_rel = max(best_rel, *(_value_match(rel.of, cm) for cm in candidate.co_mentions), 0.0)
        add("relationship", best_rel, _W_RELATIONSHIP)

    for key, want in brief.discriminators().items():
        if key in _AGE_KEYS:
            add(f"attr:{key}", _age_match(want, candidate.attributes), _W_AGE)
        else:
            have = candidate.attributes.get(key, "")
            weight = _ATTR_WEIGHTS.get(key, _W_ATTR_DEFAULT)
            add(f"attr:{key}", _value_match(want, have) if have else 0.0, weight)

    score = achieved / possible if possible > 0 else 0.0
    # Keep only signals that actually fired — cleaner evidence trail.
    fired = {k: round(v, 3) for k, v in signals.items() if v > 0}
    return MatchScore(score=round(score, 3), signals=fired)


def propose_enrichments(brief: TargetBrief, candidate: Candidate) -> dict[str, str]:
    """Attributes a matched candidate teaches us that the brief doesn't yet know
    (the OBSERVE→REFINE feedback of §4.2). The orchestrator applies these via
    `brief.with_attribute` / `with_handle` — but only for above-threshold
    candidates, so noise from a wrong match never pollutes the brief.

    Includes an inferred **given name** when the brief's is unknown: the token of
    the candidate's name that isn't the (known) surname.
    """
    out: dict[str, str] = {}
    for key, val in candidate.attributes.items():
        if key not in brief.known_attributes and val.strip():
            out[key] = val.strip()
    if not brief.given_name and brief.surname:
        surname_tokens = set(_tokens(brief.surname))
        extra = [t for t in _tokens(candidate.name) if t not in surname_tokens]
        if len(extra) == 1:                 # exactly one leftover token ⇒ the given name
            # Recover the original-cased token from the candidate name.
            for raw in candidate.name.split():
                if _norm(raw) == extra[0]:
                    out["given_name"] = raw
                    break
    return out
