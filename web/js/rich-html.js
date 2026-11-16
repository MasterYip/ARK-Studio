import { Lexer } from "../vendor/marked/marked.esm.js";
import DOMPurify from "../vendor/dompurify/purify.es.js";

// Keep raw HTML tags together, including pairs surrounding Markdown blocks.
export function htmlBlockTokens(text) {
  if (!text.includes("<")) return null;
  const tokens = Lexer.lex(text);
  const containsHtml = (items) => items.some((token) => token.type === "html"
    || (token.tokens && containsHtml(token.tokens)) || (token.items && containsHtml(token.items)));
  return containsHtml(tokens) ? tokens : null;
}

export function inlineHtmlTokens(text) {
  if (!text.includes("<")) return null;
  const tokens = Lexer.lexInline(text);
  return tokens.some((token) => token.type === "html") ? tokens : null;
}

export function sanitizeRichHtml(source) {
  const fragment = DOMPurify.sanitize(source, {
    USE_PROFILES: { html: true },
    RETURN_DOM_FRAGMENT: true,
    ALLOW_DATA_ATTR: false,
    ADD_TAGS: ["ark-md-slot"],
    ADD_ATTR: ["data-ark-slot"],
    FORBID_TAGS: ["style", "form", "input", "button", "select", "textarea", "iframe", "object", "embed"],
    FORBID_ATTR: ["class", "id", "name", "srcset", "autoplay"],
  });
  // Parse CSS with the browser; only colour declarations reach ARK's document.
  for (const element of fragment.querySelectorAll("[style]")) {
    const parsed = document.createElement("span").style;
    parsed.cssText = element.getAttribute("style");
    element.removeAttribute("style");
    for (const property of ["color", "background-color", "border-color", "border-top-color",
      "border-right-color", "border-bottom-color", "border-left-color", "outline-color", "text-decoration-color"]) {
      const value = parsed.getPropertyValue(property);
      if (value) element.style.setProperty(property, value);
    }
  }
  return fragment;
}
