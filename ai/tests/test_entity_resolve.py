"""Offline unit tests for entity resolution (docs/15 §4.2)."""

from __future__ import annotations

from app.entity_brief import Relationship, TargetBrief
from app.entity_resolve import (
    DEFAULT_MATCH_THRESHOLD,
    Candidate,
    propose_enrichments,
    score_candidate,
)


def _brief(**kw) -> TargetBrief:
    return TargetBrief(**kw)


# ------------------------------------------------------------- basic scoring ---

def test_perfect_match_scores_high():
    b = _brief(surname="Rossi", given_name="Giulia",
               known_attributes={"school": "Liceo Volta", "city": "Como"})
    c = Candidate(name="Giulia Rossi",
                  attributes={"school": "Liceo A. Volta", "city": "Como"})
    ms = score_candidate(b, c)
    assert ms.score >= 0.95
    assert ms.is_match()
    assert set(ms.signals) >= {"surname", "given_name", "attr:school", "attr:city"}


def test_wrong_person_scores_low():
    b = _brief(surname="Rossi", given_name="Giulia",
               known_attributes={"school": "Liceo Volta", "city": "Como"})
    c = Candidate(name="Marco Bianchi",
                  attributes={"school": "Liceo Manzoni", "city": "Milano"})
    ms = score_candidate(b, c)
    assert ms.score < DEFAULT_MATCH_THRESHOLD
    assert not ms.is_match()


def test_empty_brief_scores_zero():
    ms = score_candidate(_brief(), Candidate(name="Anyone"))
    assert ms.score == 0.0
    assert ms.signals == {}


def test_score_is_fraction_of_known_signals():
    # Only surname known; a surname-only candidate should score ~1.0 (it met
    # everything the brief knew), not be penalized for unspecified fields.
    b = _brief(surname="Rossi")
    ms = score_candidate(b, Candidate(name="Giulia Rossi"))
    assert ms.score >= 0.95


# --------------------------------------------------------- the relationship ---

def test_relationship_corroboration_is_decisive():
    # Two same-surname candidates; only the one co-mentioned with the anchor
    # (the friend) should clear the bar — the namesake disambiguator (§4.2).
    b = _brief(surname="Rossi",
               relationships=[Relationship("sibling_of", "Marco Rossi")])
    target = Candidate(name="Giulia Rossi", co_mentions=["Marco Rossi", "Luca"])
    namesake = Candidate(name="Giulia Rossi", co_mentions=["Paolo Verdi"])
    assert score_candidate(b, target).score > score_candidate(b, namesake).score
    assert score_candidate(b, target).is_match()


def test_relationship_missing_comention_does_not_crash():
    b = _brief(surname="Rossi",
               relationships=[Relationship("sibling_of", "Marco Rossi")])
    ms = score_candidate(b, Candidate(name="Giulia Rossi", co_mentions=[]))
    assert "relationship" not in ms.signals   # fired signals only; rel=0 dropped


# --------------------------------------------------------- fuzzy / accents ----

def test_accent_and_spelling_tolerance():
    b = _brief(surname="Rossì", known_attributes={"city": "Torino"})
    c = Candidate(name="G. ROSSI", attributes={"city": "Torìno"})
    ms = score_candidate(b, c)
    assert ms.signals["surname"] == 1.0     # accent-insensitive token match
    assert ms.signals["attr:city"] == 1.0


def test_handle_contains_name():
    b = _brief(surname="Rossi", given_name="Giulia")
    c = Candidate(name="", handle="@giuliarossi")
    ms = score_candidate(b, c)
    assert ms.signals["surname"] >= 0.9
    assert ms.signals["given_name"] >= 0.9


def test_substring_attribute_match():
    b = _brief(surname="Rossi", known_attributes={"school": "Volta"})
    c = Candidate(name="Rossi", attributes={"school": "Liceo Scientifico A. Volta"})
    assert score_candidate(b, c).signals["attr:school"] == 1.0


# ------------------------------------------------------------------- age ------

def test_age_within_tolerance():
    b = _brief(surname="Rossi", known_attributes={"birth_year": "2003"})
    near = Candidate(name="Rossi", attributes={"birth_year": "2004"})
    far = Candidate(name="Rossi", attributes={"birth_year": "1975"})
    assert score_candidate(b, near).signals["attr:birth_year"] > 0
    assert "attr:birth_year" not in score_candidate(b, far).signals  # 0 → dropped


# ------------------------------------------------------------- enrichments ----

def test_propose_enrichments_learns_new_attributes():
    b = _brief(surname="Rossi", known_attributes={"city": "Como"})
    c = Candidate(name="Giulia Rossi",
                  attributes={"city": "Como", "employer": "Acme"})
    enr = propose_enrichments(b, c)
    assert enr.get("employer") == "Acme"      # new
    assert "city" not in enr                   # already known — not re-proposed
    assert enr.get("given_name") == "Giulia"   # inferred leftover token


def test_propose_enrichments_no_given_name_when_ambiguous():
    # Two leftover tokens ⇒ can't safely infer a single given name.
    b = _brief(surname="Rossi")
    c = Candidate(name="Giulia Maria Rossi")
    assert "given_name" not in propose_enrichments(b, c)


def test_propose_enrichments_respects_known_given_name():
    b = _brief(surname="Rossi", given_name="Anna")
    c = Candidate(name="Giulia Rossi")
    assert "given_name" not in propose_enrichments(b, c)
