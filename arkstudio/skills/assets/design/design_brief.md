# Design Brief · $project_name

## Context

- Audience: $audience
- Venue / delivery: $venue
- Mediums: 
$mediums

## Intent

Three specific qualities this work should project:

$qualities

## Anti-goals

$anti_goals

## Semantics before styling

List the recurring meanings before choosing colours: primary method,
observations, actions, baselines, uncertainty, success, warning, failure,
neutral structure and focal contribution. Map each meaning to a semantic token
in `theme.json`; do not invent appearance names.

## Theme

- Theme: `$theme_name` version `$theme_version`
- Extend an existing theme or create a new one; record the decision.
- Validate the theme before downstream styling:
  `POST /api/skills/design/validate` with `{kind: "theme", theme: {...}}`.

## Output

- Primary artifact: `$artifact`
- Prefer editable SVG/PDF; generate raster previews secondarily.
- Keep data processing and presentation code separate.
- Verify at delivery size and in grayscale before attaching the artifact to an
  ARK artifact unit.
