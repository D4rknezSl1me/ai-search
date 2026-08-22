"""Offline unit tests for the entity-discovery target brief (docs/15 §3)."""

from __future__ import annotations

from app.entity_brief import Budget, Goal, Relationship, TargetBrief


# ------------------------------------------------------------------ parsing ---

def test_from_dict_full_brief():
    b = TargetBrief.from_dict({
        "goal": "social_handle",
        "subject": {
            "surname": "Rossi",
            "given_name": None,
            "known_attributes": {"School": " Liceo Volta ", "city": "Como", "blank": ""},
            "relationships": [{"type": "sibling_of", "of": "Marco Rossi"}],
            "seed_handles": ["@marco", ""],
        },
        "constraints": {
            "platforms": ["Instagram", "tiktok"],
            "budget": {"max_hops": 6, "max_fetches": 50, "max_wall_s": 300},
        },
    })
    assert b.goal is Goal.SOCIAL_HANDLE
    assert b.surname == "Rossi"
    assert b.given_name == ""                     # unknown ⇒ the objective
    assert b.known_attributes == {"school": "Liceo Volta", "city": "Como"}  # blank dropped
    assert b.relationships == [Relationship("sibling_of", "Marco Rossi")]
    assert b.seed_handles == ["@marco"]           # blank dropped
    assert "open_web" in b.platforms              # always appended as the floor
    assert "instagram" in b.platforms and "tiktok" in b.platforms
    assert b.budget == Budget(max_hops=6, max_fetches=50, max_wall_s=300)


def test_from_dict_defaults_and_bad_input():
    b = TargetBrief.from_dict({})
    assert b.goal is Goal.ANY_INFO               # unknown goal ⇒ broadest
    assert b.platforms == ("open_web",)
    assert b.budget == Budget()                  # defaults
    # Bad goal string still parses to ANY_INFO, never throws.
    assert TargetBrief.from_dict({"goal": "nonsense"}).goal is Goal.ANY_INFO


def test_from_dict_accepts_flat_subject():
    # Some callers omit the "subject" wrapper — attributes live at top level.
    b = TargetBrief.from_dict({"surname": "Hopper", "given_name": "Grace"})
    assert b.full_name == "Grace Hopper"
    assert b.is_resolved_name


def test_relationship_requires_both_fields():
    assert Relationship.from_dict({"type": "sibling_of"}) is None
    assert Relationship.from_dict({"of": "x"}) is None
    assert Relationship.from_dict("nope") is None
    assert Relationship.from_dict({"type": "a", "of": "b"}) == Relationship("a", "b")


def test_budget_rejects_nonpositive_and_garbage():
    bud = Budget.from_dict({"max_hops": 0, "max_fetches": -5, "max_wall_s": "x"})
    assert bud == Budget()  # falls back to defaults for each bad field


# --------------------------------------------------------------- enrichment ---

def test_with_attribute_promotes_name_parts():
    b = TargetBrief(surname="Rossi")
    b2 = b.with_attribute("given_name", "Giulia")
    assert b2.given_name == "Giulia"
    assert b2.full_name == "Giulia Rossi"
    # Original is unchanged (immutably-style copy).
    assert b.given_name == ""


def test_with_attribute_never_overwrites_known():
    b = TargetBrief(given_name="Giulia", known_attributes={"city": "Como"})
    assert b.with_attribute("given_name", "Anna").given_name == "Giulia"   # first wins
    assert b.with_attribute("city", "Milano").known_attributes["city"] == "Como"


def test_with_attribute_adds_new_free_form():
    b = TargetBrief(surname="Rossi")
    b2 = b.with_attribute("employer", "Acme")
    assert b2.known_attributes == {"employer": "Acme"}


def test_with_attribute_ignores_blank():
    b = TargetBrief(surname="Rossi")
    assert b.with_attribute("", "x") is b
    assert b.with_attribute("employer", "  ") is b


def test_with_handle_dedupes():
    b = TargetBrief(seed_handles=["@a"])
    assert b.with_handle("@a").seed_handles == ["@a"]
    assert b.with_handle("@b").seed_handles == ["@a", "@b"]


# ------------------------------------------------------------- derived views ---

def test_discriminators_and_full_name():
    b = TargetBrief(surname="Rossi", known_attributes={"school": "Volta"})
    assert b.discriminators() == {"school": "Volta"}
    assert b.full_name == "Rossi"
    assert not b.is_resolved_name
