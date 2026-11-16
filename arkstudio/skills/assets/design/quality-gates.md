# Quality Gates (ARK)

Adapted from `references/quality-gates.md` of the vendor skill. The API returns
these as data from `validate_theme()` and `verify_artifact()`.

## Shared

- Theme validates and every artifact names its theme version.
- No unexplained local colors, opacity, font sizes, radii or motion curves.
- Hierarchy survives grayscale and meaning does not rely only on hue.
- Text, equations, labels and annotations remain readable at delivery size.

## Paper

- Data transformation and uncertainty are auditable.
- Axes, scales, legends, units and panel labels are unambiguous.
- SVG/PDF is unclipped, fonts render correctly, and final-width print works.

## Website

- Keyboard focus, headings, alternative text, contrast and reduced motion pass.
- Mobile order is preserved and media does not cause layout shift.
- Generated HTML has a viewport meta tag and discloses external assets.

## Video

- Safe area, captions, pacing, muted comprehension and end card pass.
- Resolution, frame rate, duration and encoded playback match the target.

## ARK artifact verification

`design.verify_artifact()` adds mechanical checks before the visual gates:

- the file exists, is non-empty and lives inside the ARK project;
- SVG is well-formed and self-contained;
- PNG/JPEG/PDF magic bytes are valid;
- HTML declares a viewport and lists external references;
- CSS declares custom-property tokens.
