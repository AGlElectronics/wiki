// Markdown import and export helpers. Import works through the editor: a
// page is created, the markdown is parked in sessionStorage, and the
// editor seeds the blocks over the live connection when it opens the page
// (BlockEditor reads pendingImportKey).
import { createPage } from '@/api/wiki';

export const pendingImportKey = (pageId: string) => `wiki-import:${pageId}`;

/** Creates a page from a .md file; returns the new page id to navigate to. */
export async function importMarkdownFile(spaceId: string, parentId: string | null, file: File): Promise<string> {
  const text = await file.text();
  const title = file.name.replace(/\.(md|markdown|txt)$/i, '').trim() || 'Imported page';
  const page = await createPage(spaceId, { parent_id: parentId, title });
  sessionStorage.setItem(pendingImportKey(page.id), text);
  return page.id;
}

/** Offers text as a file download. */
export function downloadText(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/markdown;charset=utf-8' }));
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  a.click();
  URL.revokeObjectURL(url);
}

/** Opens a file picker for markdown files. */
export function pickMarkdownFile(): Promise<File | null> {
  return new Promise((resolve) => {
    const input = document.createElement('input');
    input.type = 'file';
    input.accept = '.md,.markdown,.txt,text/markdown';
    input.onchange = () => resolve(input.files?.[0] ?? null);
    input.oncancel = () => resolve(null);
    input.click();
  });
}
