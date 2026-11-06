// BlockNote is large, so it loads on first page view (see PageEditor).
import { useEffect, useMemo } from 'react';
import { CommentsExtension } from '@blocknote/core/comments';
import { withCollaboration } from '@blocknote/core/yjs';
import {
  FloatingComposerController,
  FloatingThreadController,
  SuggestionMenuController,
  useCreateBlockNote,
  type DefaultReactSuggestionItem,
} from '@blocknote/react';
import { BlockNoteView } from '@blocknote/mantine';
import '@blocknote/core/fonts/inter.css';
import '@blocknote/mantine/style.css';
import {
  atLeast,
  fetchFileBlob,
  peopleByIds,
  searchPages,
  searchPeople,
  uploadPageFile,
  type Level,
} from '@/api/wiki';
import { collectMentionLinks, wikiSchema } from '@/components/editorSchema';
import { useMe } from '@/hooks/useMe';
import { useThemePreference } from '@/lib/theme';
import type { WikiProvider } from '@/live/WikiProvider';
import { WikiThreadStore } from '@/live/WikiThreadStore';
import { userColor } from '@/live/userColor';

const PAGE_LINK = /\/p\/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/gi;

// File URLs in blocks are backend paths (/api/v1/files/<id>), which plain
// <img>/<a> tags cannot fetch (no auth header). The editor resolves them to
// blob object URLs, cached per session.
const blobURLs = new Map<string, string>();
async function resolveFileUrl(url: string): Promise<string> {
  if (!url.startsWith('/api/v1/files/')) return url;
  const cached = blobURLs.get(url);
  if (cached) return cached;
  const blob = await fetchFileBlob(url);
  const objectURL = URL.createObjectURL(blob);
  blobURLs.set(url, objectURL);
  return objectURL;
}

type WikiEditor = ReturnType<typeof useWikiEditor>;

const isUUID = (s: string) => /^[0-9a-f-]{36}$/i.test(s);

/** Comment authors shown by id; unknown ids render as "Someone". */
async function resolveCommentUsers(userIds: string[]) {
  const people = await peopleByIds([...new Set(userIds.filter(isUUID))]).catch(() => []);
  return userIds.map((id) => {
    const p = people.find((x) => x.id === id);
    return { id, username: p?.display_name ?? 'Someone', avatarUrl: '', color: userColor(id) };
  });
}

function useWikiEditor(provider: WikiProvider, userId: string, level: Level) {
  const threadStore = useMemo(
    () => new WikiThreadStore(provider.pageId, userId, atLeast(level, 'edit') ? 'editor' : 'comment'),
    [provider.pageId, userId, level]
  );
  return useCreateBlockNote(
    withCollaboration({
      schema: wikiSchema,
      collaboration: {
        fragment: provider.doc.getXmlFragment('document-store'),
        user: { name: 'Someone', color: '#888888' },
        provider: { awareness: provider.awareness },
        showCursorLabels: 'activity',
      },
      uploadFile: async (file: File) => (await uploadPageFile(provider.pageId, file)).url,
      resolveFileUrl,
      extensions: [CommentsExtension({ threadStore, resolveUsers: resolveCommentUsers })],
    }),
    [provider, threadStore]
  );
}

/** '@' suggestions: people, then pages, both from the backend pickers. */
async function mentionItems(editor: WikiEditor, query: string): Promise<DefaultReactSuggestionItem[]> {
  const [people, pages] = await Promise.all([
    searchPeople(query).catch(() => []),
    query.trim() ? searchPages(query).catch(() => []) : Promise.resolve([]),
  ]);
  const insert = (content: Parameters<WikiEditor['insertInlineContent']>[0]) => {
    editor.insertInlineContent(content);
  };
  return [
    ...people.slice(0, 5).map((p) => ({
      title: p.display_name,
      subtext: p.email,
      group: 'People',
      onItemClick: () => insert([{ type: 'personMention', props: { name: p.display_name, userId: p.id } }, ' ']),
    })),
    ...pages.slice(0, 5).map((pg) => ({
      title: `${pg.icon || '📄'} ${pg.title.trim() || 'Untitled'}`,
      subtext: pg.space_name,
      group: 'Pages',
      onItemClick: () =>
        insert([{ type: 'pageMention', props: { title: pg.title, icon: pg.icon, pageId: pg.id } }, ' ']),
    })),
  ];
}

export default function BlockEditor({
  provider,
  level,
  exportRef,
}: {
  provider: WikiProvider;
  level: Level;
  /** Filled with a markdown exporter once the editor is up (PageView menu). */
  exportRef?: React.MutableRefObject<(() => string) | null>;
}) {
  const { me } = useMe();
  const { resolved } = useThemePreference();
  const user = useMemo(
    () => ({
      name: me?.display_name || me?.email || 'Someone',
      color: userColor(me?.id ?? 'anonymous'),
      id: me?.id ?? '',
    }),
    [me?.display_name, me?.email, me?.id]
  );

  const editor = useWikiEditor(provider, me?.id ?? '', level);

  useEffect(() => {
    provider.setSummary(() => {
      const text = editor.blocksToMarkdownLossy();
      const links = new Set<string>();
      for (const m of text.matchAll(PAGE_LINK)) links.add(m[1].toLowerCase());
      collectMentionLinks(editor.document, links);
      return { text, links: Array.from(links) };
    });
  }, [editor, provider]);

  // The cursor label follows the profile once /me loads.
  useEffect(() => {
    provider.awareness.setLocalStateField('user', user);
  }, [provider, user]);

  // Markdown export for the page menu.
  useEffect(() => {
    if (!exportRef) return;
    exportRef.current = () => editor.blocksToMarkdownLossy();
    return () => {
      exportRef.current = null;
    };
  }, [editor, exportRef]);

  // A freshly imported markdown file waits in sessionStorage (lib/markdown):
  // seed the blocks once the editor is live, so they flow through the
  // normal collaboration path and get saved.
  useEffect(() => {
    if (!atLeast(level, 'edit')) return;
    const key = `wiki-import:${provider.pageId}`;
    const md = sessionStorage.getItem(key);
    if (!md) return;
    sessionStorage.removeItem(key);
    void (async () => {
      const blocks = await editor.tryParseMarkdownToBlocks(md);
      if (blocks.length > 0) editor.replaceBlocks(editor.document, blocks);
    })();
  }, [editor, provider.pageId, level]);

  return (
    <BlockNoteView
      editor={editor}
      editable={atLeast(level, 'edit')}
      theme={resolved}
      className="wiki-editor md:-mx-[54px]"
    >
      <SuggestionMenuController
        triggerCharacter="@"
        getItems={(query) => mentionItems(editor, query)}
      />
      <FloatingComposerController />
      <FloatingThreadController />
    </BlockNoteView>
  );
}
