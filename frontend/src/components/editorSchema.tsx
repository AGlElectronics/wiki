// The wiki's BlockNote schema: the default blocks plus @person and @page
// mentions as inline content. Shared by the live editor and the read-only
// revision preview, so old documents always render.
import { BlockNoteSchema, defaultInlineContentSpecs } from '@blocknote/core';
import { createReactInlineContentSpec } from '@blocknote/react';

export const PersonMention = createReactInlineContentSpec(
  {
    type: 'personMention',
    propSchema: {
      name: { default: '' },
      userId: { default: '' },
    },
    content: 'none',
  },
  {
    render: (props) => (
      <span className="rounded bg-primary-soft px-1 text-primary font-medium">
        @{props.inlineContent.props.name || 'someone'}
      </span>
    ),
  }
);

export const PageMention = createReactInlineContentSpec(
  {
    type: 'pageMention',
    propSchema: {
      title: { default: '' },
      icon: { default: '' },
      pageId: { default: '' },
    },
    content: 'none',
  },
  {
    render: (props) => (
      <a
        href={`/p/${props.inlineContent.props.pageId}`}
        className="rounded bg-secondary px-1 font-medium text-foreground no-underline hover:underline"
      >
        {props.inlineContent.props.icon || '📄'} {props.inlineContent.props.title.trim() || 'Untitled'}
      </a>
    ),
  }
);

export const wikiSchema = BlockNoteSchema.create({
  inlineContentSpecs: {
    ...defaultInlineContentSpecs,
    personMention: PersonMention,
    pageMention: PageMention,
  },
});

type InlineItem = { type?: string; props?: Record<string, unknown> };
type BlockLike = { content?: unknown; children?: BlockLike[] };

/** Page ids referenced by @page mentions, for the backlinks index. */
export function collectMentionLinks(blocks: BlockLike[], into: Set<string>) {
  for (const b of blocks) {
    if (Array.isArray(b.content)) {
      for (const ic of b.content as InlineItem[]) {
        if (ic?.type === 'pageMention' && typeof ic.props?.pageId === 'string' && ic.props.pageId) {
          into.add((ic.props.pageId as string).toLowerCase());
        }
      }
    }
    if (b.children?.length) collectMentionLinks(b.children, into);
  }
}
