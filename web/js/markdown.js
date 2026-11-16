import { Marked, Lexer } from "../vendor/marked/marked.esm.js";

// Let Marked handle Markdown delimiters; ARK only adds its domain syntax.
const markdown = new Marked({ gfm: true, breaks: true }, {
  extensions: [
    {
      name: "arkReference", level: "inline",
      start: (source) => source.search(/\\?\[\[/),
      tokenizer(source) {
        const match = /^(\\?)\[\[([A-Za-z0-9][A-Za-z0-9_-]*)\]\]/.exec(source);
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
      name: "arkMathBlock", level: "block", start: (source) => source.indexOf("$$"),
      tokenizer(source) {
        const match = /^ {0,3}\$\$([\s\S]*?)\$\$[ \t]*(?:\n|$)/.exec(source);
        if (match) return { type: "arkMathBlock", raw: match[0], text: match[1].trim() };
      },
    },
  ],
});

export function markdownTokens(source) { return markdown.lexer(source); }
export function markdownInlineTokens(source) { return Lexer.lexInline(source, markdown.defaults); }
