-- +goose Up
-- Inline discussions. A thread anchors to a page (the editor marks the
-- exact text range inside the Yjs document with the thread id; the mark
-- travels with the text). Comment bodies are small BlockNote documents
-- (JSONB), never Yjs. Deleting a comment is a soft delete so a thread
-- keeps its shape; deleting the thread removes everything.
CREATE TABLE comment_threads (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    page_id     UUID NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
    created_by  UUID REFERENCES users(id) ON DELETE SET NULL,
    resolved    BOOLEAN NOT NULL DEFAULT FALSE,
    resolved_by UUID REFERENCES users(id) ON DELETE SET NULL,
    resolved_at TIMESTAMPTZ,
    metadata    JSONB NOT NULL DEFAULT 'null',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX comment_threads_page_idx ON comment_threads (page_id, created_at);

CREATE TABLE comments (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id  UUID NOT NULL REFERENCES comment_threads(id) ON DELETE CASCADE,
    created_by UUID REFERENCES users(id) ON DELETE SET NULL,
    body       JSONB,
    metadata   JSONB NOT NULL DEFAULT 'null',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CHECK (body IS NOT NULL OR deleted_at IS NOT NULL)
);
CREATE INDEX comments_thread_idx ON comments (thread_id, created_at);

CREATE TABLE comment_reactions (
    comment_id UUID NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    emoji      TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (comment_id, user_id, emoji)
);

-- +goose Down
DROP TABLE comment_reactions;
DROP TABLE comments;
DROP TABLE comment_threads;
