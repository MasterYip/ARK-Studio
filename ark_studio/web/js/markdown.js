import { Marked, Lexer } from "../vendor/marked/marked.esm.js";

// Let Marked handle Markdown delimiters; ARK only adds its domain syntax.
const markdown = new Marked({ gfm: true, breaks: true }, {
  extensions: [
    {
      name: "arkReference", level: "inline",
      start: (source) => source.search(/\\?\[\[/),
      tokenizer(source) {
        const match = /^(\\?)\[\[((?:prj_[A-Za-z0-9]+\/)?[A-Za-z0-9][A-Za-z0-9_-]*)\]\]/.exec(source);
        if (match) return { type: match[1] ? "arkLiteral" : "arkReference", raw: match[0], text: match[2] };
      },
    },
    {
      name: "arkMath", level: "inline", start: (source) => source.indexOf("$"),
      tokenizer(source) {
        const match = /^\$([^$\n]+)\$/.exec(source);
        if (match) return { type: "arkMath", raw: match[0], text: match[1] };
      },
    },
    {
      name: "arkCitation", level: "inline", start: (source) => source.search(/\[\d{1,3}\]/),
      tokenizer(source) {
        const match = /^\[(\d{1,3})\](?![([])/.exec(source);
        if (match && !this.lexer.tokens.links?.[match[1]]) return { type: "arkCitation", raw: match[0] };
      },
    },
    {
      name: "arkMedia", level: "inline", start: (source) => source.indexOf("/api/media/"),
      tokenizer(source) {
        const match = /^\/api\/media\/\d+\/raw\b/.exec(source);
        if (match) return { type: "arkMedia", raw: match[0], href: match[0], text: match[0] };
      },
    },
    {
      // A bare task-space path (task:<relative path>) is a link, like a bare URL.
      name: "arkTaskPath", level: "inline",
      start(source) {
        const match = /(^|[^\w/.:-])task:[^\s<]/i.exec(source);
        return match ? match.index + match[1].length : undefined;
      },
      tokenizer(source) {
        const match = /^task:[^\s<>()[\]`"']+/i.exec(source);
        if (!match) return undefined;
        const href = bareUrlEnd(match[0]);
        if (href.length <= 5) return undefined;
        return { type: "link", raw: href, text: href, href, tokens: [{ type: "text", raw: href, text: href }] };
      },
    },
    {
      name: "arkMathBlock", level: "block", start: (source) => source.indexOf("$$"),
      tokenizer(source) {
        const match = /^ {0,3}\$\$([\s\S]*?)\$\$[ \t]*(?:\n|$)/.exec(source);
        if (match) return { type: "arkMathBlock", raw: match[0], text: match[1].trim() };
      },
    },
  ],
});

// A bare URL ends at the first CJK character or full-width punctuation:
// "https://a.example:8443（Let's …" links only the address.  Marked's GFM
// autolink stops at whitespace only, which Chinese text rarely has.
const CJK_STOP = /[‘-‟…　-〿㐀-䶿一-鿿＀-￯]/;
export function bareUrlEnd(candidate) {
  const cut = candidate.search(CJK_STOP);
  let url = cut < 0 ? candidate : candidate.slice(0, cut);
  url = url.replace(/[.,;:!?'"*_~]+$/, "");
  // Keep a closing ")" only when the URL itself opened one.
  while (url.endsWith(")") && (url.match(/\(/g) || []).length < (url.match(/\)/g) || []).length) url = url.slice(0, -1);
  return url;
}
markdown.use({
  tokenizer: {
    url(src) {
      const match = /^(?:https?:\/\/|www\.)[^\s<]+/i.exec(src);
      if (!match || !CJK_STOP.test(match[0])) return false; // Marked's own rule
      const url = bareUrlEnd(match[0]);
      if (!url || /^(?:https?:\/\/|www\.)$/i.test(url)) return false;
      const href = /^www\./i.test(url) ? `http://${url}` : url;
      return { type: "link", raw: url, text: url, href, tokens: [{ type: "text", raw: url, text: url }] };
    },
  },
});

export function markdownTokens(source) { return markdown.lexer(source); }
export function markdownInlineTokens(source) { return Lexer.lexInline(source, markdown.defaults); }
