// collectMentionLinks feeds the backlinks index from @page mentions.
import { collectMentionLinks } from '@/components/editorSchema';

test('collects page mention ids, lowercased, through nested blocks', () => {
  const links = new Set<string>();
  collectMentionLinks(
    [
      {
        content: [
          { type: 'text' },
          { type: 'pageMention', props: { pageId: 'AAAAAAAA-0000-0000-0000-000000000001', title: 'X' } },
        ],
        children: [
          {
            content: [{ type: 'pageMention', props: { pageId: 'bbbbbbbb-0000-0000-0000-000000000002' } }],
          },
        ],
      },
      { content: 'not-an-array' },
      { content: [{ type: 'pageMention', props: {} }] }, // no id: ignored
    ],
    links
  );
  expect([...links].sort()).toEqual([
    'aaaaaaaa-0000-0000-0000-000000000001',
    'bbbbbbbb-0000-0000-0000-000000000002',
  ]);
});
