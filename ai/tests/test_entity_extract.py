"""Offline unit tests for candidate extraction (docs/15 §4.2)."""

from __future__ import annotations

from app.entity_brief import TargetBrief
from app.entity_extract import _handle_from_url, extract_candidates


def _brief(**kw) -> TargetBrief:
    return TargetBrief(**kw)


# --------------------------------------------------------- anchored on name ---

def test_extracts_name_anchored_on_surname():
    b = _brief(surname="Rossi", known_attributes={"school": "Liceo Volta", "city": "Como"})
    text = ("Il liceo ha premiato Giulia Rossi, studentessa del Liceo Volta "
            "di Como, insieme al fratello Marco Rossi.")
    cands = extract_candidates(b, text=text, url="https://ex.com/news")
    assert cands, "expected at least one candidate"
    top = cands[0]
    assert "Rossi" in top.name
    assert top.attributes.get("school") == "Liceo Volta"   # corroborated nearby
    assert top.attributes.get("city") == "Como"
    assert any("Marco Rossi" in cm for cm in top.co_mentions)  # co-mention captured
    assert top.source_url == "https://ex.com/news"


def test_ignores_capitalized_noise_without_the_anchor():
    b = _brief(surname="Rossi")
    text = "The United Nations met in New York. Barack Obama spoke."
    assert extract_candidates(b, text=text) == []   # no "Rossi" anywhere


def test_name_does_not_span_line_breaks():
    # A heading followed by more capitalized text on the next line must not merge
    # into one bogus name (regression from live verification).
    b = _brief(surname="Lovelace")
    text = "Ada Lovelace\nThe Right Honourable Countess of Lovelace was a mathematician."
    cands = extract_candidates(b, text=text)
    assert cands
    assert "\n" not in cands[0].name
    assert cands[0].name == "Ada Lovelace"


def test_title_is_scanned_too():
    b = _brief(surname="Rossi")
    cands = extract_candidates(b, text="body text", title="Giulia Rossi — profile")
    assert cands and "Rossi" in cands[0].name


def test_dedupes_repeated_name():
    b = _brief(surname="Rossi")
    text = "Giulia Rossi ... later Giulia Rossi again ... and Giulia Rossi."
    cands = extract_candidates(b, text=text)
    names = [c.name for c in cands]
    assert len(names) == len(set(names))


def test_max_candidates_cap():
    b = _brief(surname="Rossi")
    text = "Giulia Rossi and Marco Rossi and Anna Rossi and Luca Rossi met."
    assert len(extract_candidates(b, text=text, max_candidates=2)) == 2


# --------------------------------------------------------------- age / year ---

def test_birth_year_extracted_from_context():
    b = _brief(surname="Rossi", known_attributes={"birth_year": "2003"})
    cands = extract_candidates(b, text="Giulia Rossi, nata nel 2003, vive a Como.")
    assert cands[0].attributes.get("birth_year") == "2003"


# ---------------------------------------------------------------- handles -----

def test_handle_from_profile_url():
    assert _handle_from_url("https://instagram.com/giulia.rossi") == "@giulia.rossi"
    assert _handle_from_url("https://www.tiktok.com/@giuliarossi") == "@giuliarossi"
    assert _handle_from_url("https://example.com/whatever") == ""   # not a profile host


def test_handle_from_url_attached_to_candidate():
    b = _brief(surname="Rossi")
    cands = extract_candidates(b, text="Profile of Giulia Rossi.",
                               url="https://instagram.com/giulia.rossi",
                               platform="instagram")
    assert cands[0].handle == "@giulia.rossi"
    assert cands[0].platform == "instagram"


def test_at_mention_handle_when_no_url_handle():
    b = _brief(surname="Rossi")
    cands = extract_candidates(b, text="Follow Giulia Rossi at @giuliar on socials.")
    assert cands[0].handle == "@giuliar"


def test_profile_url_matching_surname_emitted_even_without_name_in_body():
    # Handle contains the surname but the body never spells the name → still a lead.
    b = _brief(surname="rossi")
    cands = extract_candidates(b, text="just some unrelated words",
                               url="https://instagram.com/rossi",
                               platform="instagram")
    assert cands and cands[0].handle == "@rossi"
