// Comment bodies are stored in BlockNote's block format, so inline comments
// (written by BlockNote's composer) and page-level discussions share it.

/** A comment body with one paragraph per line. */
export function textToBody(text: string) {
  return text.split('\n').map((line) => ({ type: 'paragraph', content: [{ type: 'text', text: line, styles: {} }] }));
}

/** Plain text of a comment body: the text of each block, one per line. */
export function bodyToText(body: unknown): string {
  if (!Array.isArray(body)) return '';
  return body
    .map((block) => {
      const content = (block as { content?: unknown }).content;
      if (typeof content === 'string') return content;
      if (!Array.isArray(content)) return '';
      return content
        .map((c) => {
          const text = (c as { text?: unknown }).text;
          return typeof text === 'string' ? text : '';
        })
        .join('');
    })
    .join('\n');
}
