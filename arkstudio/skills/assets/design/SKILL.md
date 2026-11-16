# Academic Design Workflow · ARK adaptation

Adapted from the standalone `academic-design-workflow` skill (MIT). One
versioned theme is the source of truth for every medium. ARK implements the
same contract with the standard library and JSON, so a theme can be validated
inside the workbench without installing pydantic, PyYAML, Matplotlib or
After Effects tooling.

## Start

1. Inspect the scientific content, audience, venue, existing assets and final
   output constraints.
2. Decide whether to extend an existing theme or create a new one. Record
   intended qualities and anti-goals in `meta.intent` and `meta.avoid`.
3. Validate the theme before styling: `POST /api/skills/design/validate`.
4. Create low-cost structural candidates before polishing an important
   artifact.
5. Implement from semantic tokens; no isolated magic colours, sizes, radii or
   motion curves.
6. Verify the artifact at its delivery size, then attach it to an ARK
   `artifact` unit.

## Invariants

- Semantics precede aesthetics: content relationships determine layout and
  emphasis.
- Use semantic roles such as `data_primary`, never appearance names such as
  `blue_box`.
- Encode critical distinctions with shape, label, marker or line style as well
  as hue.
- Keep data processing auditable and separate from presentation code.
- Prefer editable SVG/PDF for paper figures; generate raster previews
  secondarily.
- Do not imitate a reference artifact literally. Extract principles, disclose
  the reference, and produce an original grammar for the user's content.
- If a downstream need is absent from the schema, extend and document the theme
  instead of bypassing it locally.

## ARK integration

| Design object | ARK location |
|---|---|
| theme | project `assets/design/theme.json` or a validated JSON payload |
| figure/website/video artifact | `assets/...` attached to an `artifact` unit |
| design brief | `POST /api/skills/design/render` template `design_brief.md` |
| style review | template `style_review.md` + `verify_artifact()` |
| source/reference asset | `assets/` with disclosure; never a copied URL |

Source rules:

- A referenced image is evidence for a principle, not a file to copy. Record the
  reference and produce an original asset.
- Design inputs stay project-relative under `assets/`; external URLs are
  rejected by `validate_asset_path` and flagged in generated SVG/HTML.
- Keep raw data, processing code and presentation code separable so a figure can
  be regenerated.

## Quality gates

Use the shared, paper, website and video gates in `quality-gates.md`. The
verification API returns the same gates as structured data so a reviewer can
tick them without re-reading prose.
