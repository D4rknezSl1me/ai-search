"""Offline unit tests for attribute-anchored query generation (docs/15 §4.1)."""

from __future__ import annotations

from app.entity_brief import Goal, Relationship, TargetBrief
from app.entity_queries import generate_queries


def _texts(brief, **kw):
    return [q.text for q in generate_queries(brief, **kw)]


# ------------------------------------------------------ the guardrail (§5) ---

def test_every_query_carries_a_discriminator():
    b = TargetBrief(
        surname="Rossi",
        known_attributes={"school": "Liceo Volta", "city": "Como"},
    )
    qs = generate_queries(b)
    assert qs, "expected some queries"
    # Each query must contain at least one discriminating signal (school or city),
    # so a common surname is never fanned out unanchored.
    for q in qs:
        assert ("Liceo Volta" in q.text) or ("Como" in q.text)


def test_bare_common_surname_not_fanned_out():
    # No discriminators at all → only the plain name query, nothing speculative.
    b = TargetBrief(surname="Smith")
    qs = generate_queries(b)
    assert [q.text for q in qs] == ["Smith"]
    assert qs[0].specificity == 0


def test_no_name_yields_no_queries():
    b = TargetBrief(known_attributes={"school": "Volta"})
    assert generate_queries(b) == []


# ------------------------------------------------------------- composition ---

def test_relationship_becomes_an_anchor():
    b = TargetBrief(
        surname="Rossi",
        relationships=[Relationship("sibling_of", "Marco Rossi")],
    )
    texts = _texts(b)
    assert any('"Marco Rossi"' in t for t in texts)


def test_multi_word_terms_are_quoted():
    b = TargetBrief(given_name="Giulia", surname="Rossi",
                    known_attributes={"school": "Liceo Volta"})
    texts = _texts(b)
    assert any('"Giulia Rossi"' in t for t in texts)   # full name quoted
    assert any('"Liceo Volta"' in t for t in texts)    # multi-word attr quoted


def test_platform_scoped_site_dork_emitted():
    b = TargetBrief(
        surname="Rossi",
        known_attributes={"city": "Como"},
        platforms=("instagram", "open_web"),
    )
    qs = generate_queries(b)
    assert any(q.text.startswith("site:instagram.com") for q in qs)
    # The site-scoped query is the most specific (specificity 3) → ranked first.
    assert qs[0].platform == "instagram"


def test_all_discriminators_combined_query_exists():
    b = TargetBrief(surname="Rossi",
                    known_attributes={"school": "Volta", "city": "Como"})
    texts = _texts(b)
    assert any("Volta" in t and "Como" in t for t in texts)


# ---------------------------------------------------------------- ranking ----

def test_ranked_most_specific_first():
    b = TargetBrief(
        surname="Rossi",
        known_attributes={"school": "Volta", "city": "Como"},
        platforms=("instagram", "open_web"),
    )
    qs = generate_queries(b)
    specs = [q.specificity for q in qs]
    assert specs == sorted(specs, reverse=True)  # non-increasing specificity


def test_max_queries_caps_output():
    b = TargetBrief(
        surname="Rossi",
        known_attributes={"school": "Volta", "city": "Como", "employer": "Acme"},
        platforms=("instagram", "tiktok", "linkedin", "open_web"),
    )
    assert len(generate_queries(b, max_queries=5)) == 5
    assert generate_queries(b, max_queries=0) == []


def test_no_duplicate_query_texts():
    b = TargetBrief(surname="Rossi", known_attributes={"city": "Como"})
    texts = _texts(b)
    assert len(texts) == len(set(t.casefold() for t in texts))


def test_goal_terms_applied():
    b = TargetBrief(surname="Rossi", goal=Goal.PHOTOS,
                    known_attributes={"city": "Como"})
    texts = _texts(b)
    assert any("photo" in t for t in texts)
