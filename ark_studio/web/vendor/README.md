# Vendored browser libraries

ARK serves these libraries locally; no CDN is needed for Markdown rendering.

| Library | Version | Upstream | License |
| --- | --- | --- | --- |
| DOMPurify | 3.4.17 | https://github.com/cure53/DOMPurify | Apache-2.0 OR MPL-2.0 |
| Marked | 18.2.0 | https://github.com/markedjs/marked | MIT |
| jsdiff | 8.0.2 | https://github.com/kpdecker/jsdiff | BSD-3-Clause |
| Mermaid | 11.12.0 | https://github.com/mermaid-js/mermaid | MIT |
| three.js | r170 (0.170.0) | https://github.com/mrdoob/three.js | MIT |

`dompurify/purify.es.js` is the upstream `dist/purify.es.mjs`, renamed for the
same JavaScript MIME type and release caching as other ARK modules.
Marked supplies HTML token boundaries; DOMPurify sanitises every raw HTML
fragment before insertion. Each library's license is retained beside it.

`jsdiff/character.js`, `jsdiff/array.js` and `jsdiff/base.js` are the unmodified
`libesm/diff/` modules from the `diff@8.0.2` npm release. Git supplies the line
diff; jsdiff aligns similar changed lines before comparing their characters
locally, with bounded work for large inputs.

`mermaid/mermaid.esm.min.js` and `mermaid/chunks/mermaid.esm.min/*.js` are the
`dist/mermaid.esm.min.mjs` entry and its chunks from the `mermaid@11.12.0` npm
release (2.7 MB in 74 files, no source maps), renamed to `.js` with their
import paths changed to match, so the server versions and caches them like
other modules.  The page imports the 26 KB entry only when it shows a
`mermaid` block, and Mermaid then loads just the chunks of that diagram type
(a flowchart: about 610 KB, 180 KB gzip).

Existing KaTeX and d3-quadtree distributions retain their own licenses.

`three/three.module.min.js` is the unmodified `build/three.module.min.js` of the
`three@0.170.0` npm release (692 KB, 171 KB gzip). Only the graph's 3D view uses
it, and loads it on first use; it is not part of the offline app shell.
