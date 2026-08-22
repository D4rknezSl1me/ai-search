"""Target brief for agentic entity discovery (docs/15-DISCOVERY-AGENT.md §3).

The brief is the structured input that drives a targeted, multi-hop lookup:
*known* attributes about a subject plus the objective — the attribute we're
trying to find ("the social handle of the sister of a friend; I know her surname
and her school"). Every field is either a **filter** (narrows candidates) or a
**lead** (something to expand from); fields left unknown are the objective.

This module is pure and dependency-free: dataclasses + a tolerant `from_dict`
parser (the brief may arrive as JSON from the API or be emitted by the local LLM
parsing a free-text request) + `merge` for the loop's brief-enrichment step
(§4.2 — a newly-discovered attribute sharpens the next round of queries). No
network, no model; unit-testable offline.
"""

from __future__ import annotations

from dataclasses import dataclass, field, replace
from enum import Enum


class Goal(str, Enum):
    """What a successful lookup returns (docs/15 §3 `goal`)."""

    SOCIAL_HANDLE = "social_handle"
    REAL_NAME = "real_name"
    CONTACT = "contact"
    PHOTOS = "photos"
    ANY_INFO = "any_info"

    @classmethod
    def parse(cls, value: object) -> "Goal":
        """Tolerant parse — unknown/blank goals fall back to the broadest (ANY_INFO),
        which is recall-first (we never narrow the objective on bad input)."""
        if isinstance(value, Goal):
            return value
        try:
            return cls(str(value).strip().lower())
        except (ValueError, AttributeError):
            return cls.ANY_INFO


# Platforms the agent may plan hops against. "open_web" is always available
# (metasearch + crawl); the rest are credential-gated (docs/14) and degrade to
# open-web evidence until the owner supplies logins (docs/15 §7).
DEFAULT_PLATFORMS: tuple[str, ...] = ("open_web",)


@dataclass(frozen=True)
class Relationship:
    """A tie to another (findable) entity — the highest-signal disambiguator.

    "sibling_of Marco Rossi" collapses thousands of namesakes to a handful,
    because Marco is someone the system can already locate (docs/15 §3, §4.2).
    """

    type: str          # e.g. "sibling_of", "friend_of", "works_with", "child_of"
    of: str            # the anchor entity's name/handle

    @classmethod
    def from_dict(cls, d: object) -> "Relationship | None":
        if not isinstance(d, dict):
            return None
        rtype = str(d.get("type", "")).strip()
        of = str(d.get("of", "")).strip()
        if not rtype or not of:
            return None
        return cls(type=rtype, of=of)


@dataclass(frozen=True)
class Budget:
    """Hard stop for one run — recall-first is bounded, not infinite (docs/15 §5).

    The loop always terminates and returns partial results rather than nothing.
    """

    max_hops: int = 4
    max_fetches: int = 200
    max_wall_s: int = 900

    @classmethod
    def from_dict(cls, d: object) -> "Budget":
        if not isinstance(d, dict):
            return cls()
        def _pos(key: str, default: int) -> int:
            try:
                v = int(d.get(key, default))
            except (TypeError, ValueError):
                return default
            return v if v > 0 else default
        return cls(
            max_hops=_pos("max_hops", cls.max_hops),
            max_fetches=_pos("max_fetches", cls.max_fetches),
            max_wall_s=_pos("max_wall_s", cls.max_wall_s),
        )


# Known attribute keys we understand as discriminators. Anything else the caller
# supplies is still kept (as a free-form filter) — we never drop signal.
KNOWN_ATTR_KEYS: tuple[str, ...] = (
    "school", "employer", "city", "region", "country",
    "approx_age", "birth_year", "language", "profession", "username_guess",
)


def _clean_str(v: object) -> str:
    return str(v).strip() if v is not None else ""


@dataclass
class TargetBrief:
    """The structured objective for one entity-discovery run (docs/15 §3)."""

    goal: Goal = Goal.ANY_INFO
    surname: str = ""
    given_name: str = ""                                  # "" ⇒ unknown / objective
    known_attributes: dict[str, str] = field(default_factory=dict)
    relationships: list[Relationship] = field(default_factory=list)
    seed_handles: list[str] = field(default_factory=list)
    platforms: tuple[str, ...] = DEFAULT_PLATFORMS
    budget: Budget = field(default_factory=Budget)

    # ---- construction -------------------------------------------------------

    @classmethod
    def from_dict(cls, d: dict) -> "TargetBrief":
        """Parse a (possibly messy) brief dict — tolerant by design, since it may
        come from the API or from the LLM parsing free text (must not throw)."""
        d = d or {}
        subject = d.get("subject") if isinstance(d.get("subject"), dict) else d
        constraints = d.get("constraints") if isinstance(d.get("constraints"), dict) else {}

        attrs_in = subject.get("known_attributes")
        attrs: dict[str, str] = {}
        if isinstance(attrs_in, dict):
            for k, v in attrs_in.items():
                key, val = _clean_str(k).lower(), _clean_str(v)
                if key and val:
                    attrs[key] = val

        rels: list[Relationship] = []
        for r in subject.get("relationships") or []:
            rel = Relationship.from_dict(r)
            if rel is not None:
                rels.append(rel)

        handles = [h for h in (_clean_str(x) for x in subject.get("seed_handles") or []) if h]

        platforms_in = constraints.get("platforms") or []
        platforms = tuple(p for p in (_clean_str(x).lower() for x in platforms_in) if p)
        if not platforms:
            platforms = DEFAULT_PLATFORMS
        elif "open_web" not in platforms:
            # open_web is always available — it's the un-gated evidence floor.
            platforms = (*platforms, "open_web")

        return cls(
            goal=Goal.parse(d.get("goal")),
            surname=_clean_str(subject.get("surname")),
            given_name=_clean_str(subject.get("given_name")),
            known_attributes=attrs,
            relationships=rels,
            seed_handles=handles,
            platforms=platforms,
            budget=Budget.from_dict(constraints.get("budget")),
        )

    # ---- enrichment (the loop's OBSERVE→REFINE feedback, docs/15 §4.2) -------

    def with_attribute(self, key: str, value: str) -> "TargetBrief":
        """Return a copy with a newly-learned attribute merged in.

        `given_name` and `surname` are promoted to their own fields (they change
        how queries are built); everything else lands in `known_attributes`.
        Existing values are never overwritten — a corroborating observation
        shouldn't clobber a caller-supplied fact (first write wins, recall-safe).
        """
        key, value = _clean_str(key).lower(), _clean_str(value)
        if not key or not value:
            return self
        if key == "given_name" and not self.given_name:
            return replace(self, given_name=value)
        if key == "surname" and not self.surname:
            return replace(self, surname=value)
        if key in ("given_name", "surname") or key in self.known_attributes:
            return self  # already known — keep the first value
        merged = {**self.known_attributes, key: value}
        return replace(self, known_attributes=merged)

    def with_handle(self, handle: str) -> "TargetBrief":
        """Add a discovered handle as a new lead (deduped, order-preserving)."""
        handle = _clean_str(handle)
        if not handle or handle in self.seed_handles:
            return self
        return replace(self, seed_handles=[*self.seed_handles, handle])

    # ---- derived views ------------------------------------------------------

    @property
    def full_name(self) -> str:
        """The subject's name as far as it's known ('' when nothing is known)."""
        return " ".join(p for p in (self.given_name, self.surname) if p)

    @property
    def is_resolved_name(self) -> bool:
        """True once both name parts are known — a common objective (real_name)."""
        return bool(self.given_name and self.surname)

    def discriminators(self) -> dict[str, str]:
        """The subset of known attributes usable to *narrow* a candidate/query —
        i.e. everything except the objective. Used by query generation (§4.1) and
        the resolver (§4.2) to anchor on real signal.
        """
        return dict(self.known_attributes)
