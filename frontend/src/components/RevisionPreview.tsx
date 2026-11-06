// Read-only BlockNote view of one revision. Loaded lazily from the
// History panel: BlockNote is large and most visits never open history.
import { useMemo } from 'react';
import * as Y from 'yjs';
import { Awareness } from 'y-protocols/awareness';
import { withCollaboration } from '@blocknote/core/yjs';
import { useCreateBlockNote } from '@blocknote/react';
import { BlockNoteView } from '@blocknote/mantine';
import '@blocknote/core/fonts/inter.css';
import '@blocknote/mantine/style.css';
import { wikiSchema } from '@/components/editorSchema';
import { useThemePreference } from '@/lib/theme';

export default function RevisionPreview({
  data,
  resolveFileUrl,
}: {
  data: Uint8Array;
  /** Override how file URLs are fetched (the public share page does). */
  resolveFileUrl?: (url: string) => Promise<string>;
}) {
  const { resolved } = useThemePreference();
  const { doc, awareness } = useMemo(() => {
    const d = new Y.Doc();
    Y.applyUpdate(d, data);
    return { doc: d, awareness: new Awareness(d) };
  }, [data]);
  const editor = useCreateBlockNote(
    withCollaboration({
      schema: wikiSchema,
      collaboration: {
        fragment: doc.getXmlFragment('document-store'),
        user: { name: '', color: '#888888' },
        provider: { awareness },
        showCursorLabels: 'activity',
      },
      resolveFileUrl,
    }),
    [doc, resolveFileUrl]
  );
  return <BlockNoteView editor={editor} editable={false} theme={resolved} className="wiki-editor" />;
}
