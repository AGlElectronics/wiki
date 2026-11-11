package http_test

// Discussions: the comment level starts page-level threads and replies,
// anchoring to text needs edit, view only reads, authors edit their own
// comments, editors delete threads.

import (
	"testing"
)

func body(text string) map[string]any {
	return map[string]any{"body": []map[string]any{{"type": "paragraph", "content": text}}}
}

// TestComments covers the whole thread lifecycle with the three levels.
func TestComments(t *testing.T) {
	h := setupRouter(t)
	for _, id := range []identity{editor, security, other} {
		userID(t, h, id)
	}
	otherID := userID(t, h, other)

	// A space where dept-security comments and olga views.
	code, sp := call(t, h, editor, "POST", "/api/v1/spaces", map[string]any{"name": "Docs"})
	mustStatus(t, code, 201, "create space")
	spaceID := str(sp, "id")
	code, _ = call(t, h, editor, "PUT", "/api/v1/spaces/"+spaceID+"/grants",
		map[string]any{"principal_kind": "group", "principal": "dept-security", "level": "comment"})
	mustStatus(t, code, 200, "grant security comment")
	code, _ = call(t, h, editor, "PUT", "/api/v1/spaces/"+spaceID+"/grants",
		map[string]any{"principal_kind": "user", "principal": otherID, "level": "view"})
	mustStatus(t, code, 200, "grant olga view")
	code, p := call(t, h, editor, "POST", "/api/v1/spaces/"+spaceID+"/pages", map[string]any{"title": "Guide"})
	mustStatus(t, code, 201, "create page")
	page := str(p, "id")

	// The comment level cannot edit the page itself.
	code, _ = call(t, h, security, "PATCH", "/api/v1/pages/"+page, map[string]any{"title": "Nope"})
	mustStatus(t, code, 403, "comment level renames the page")

	// View cannot start threads. The comment level starts page-level
	// discussions; a thread anchored to text (the default) needs edit,
	// because the anchor is a mark in the document.
	pageLevel := func(text string) map[string]any {
		b := body(text)
		b["anchored"] = false
		return b
	}
	code, _ = call(t, h, other, "POST", "/api/v1/pages/"+page+"/threads", pageLevel("hi"))
	mustStatus(t, code, 403, "view-only starts a thread")
	code, _ = call(t, h, security, "POST", "/api/v1/pages/"+page+"/threads", body("anchored"))
	mustStatus(t, code, 403, "comment level anchors a thread to text")
	code, th := call(t, h, editor, "POST", "/api/v1/pages/"+page+"/threads", body("inline note"))
	mustStatus(t, code, 201, "editor anchors a thread to text")
	if th["anchored"] != true {
		t.Fatalf("an editor's default thread is anchored: %v", th)
	}
	code, _ = call(t, h, editor, "DELETE", "/api/v1/threads/"+str(th, "id"), nil)
	mustStatus(t, code, 204, "editor deletes the inline thread")
	code, th = call(t, h, security, "POST", "/api/v1/pages/"+page+"/threads", pageLevel("is this right?"))
	mustStatus(t, code, 201, "security starts a page-level discussion")
	if th["anchored"] != false {
		t.Fatalf("a page-level discussion is not anchored: %v", th)
	}
	threadID := str(th, "id")
	comments := list(th, "comments")
	if len(comments) != 1 {
		t.Fatalf("new thread should hold one comment, got %v", comments)
	}
	firstComment := str(comments[0], "id")

	// Everyone with view reads threads.
	code, bodyOut := call(t, h, other, "GET", "/api/v1/pages/"+page+"/threads", nil)
	mustStatus(t, code, 200, "olga reads threads")
	if len(list(bodyOut, "threads")) != 1 {
		t.Fatalf("olga should see one thread, got %v", bodyOut)
	}

	// Replies: view cannot, the editor can.
	code, _ = call(t, h, other, "POST", "/api/v1/threads/"+threadID+"/comments", body("me too"))
	mustStatus(t, code, 403, "view-only replies")
	code, th = call(t, h, editor, "POST", "/api/v1/threads/"+threadID+"/comments", body("yes it is"))
	mustStatus(t, code, 201, "editor replies")
	comments = list(th, "comments")
	if len(comments) != 2 {
		t.Fatalf("thread should hold two comments, got %v", comments)
	}
	editorComment := str(comments[1], "id")

	// Only the author edits a comment.
	code, _ = call(t, h, editor, "PATCH", "/api/v1/comments/"+firstComment, body("hacked"))
	mustStatus(t, code, 403, "editor edits security's comment")
	code, _ = call(t, h, security, "PATCH", "/api/v1/comments/"+firstComment, body("is this correct?"))
	mustStatus(t, code, 200, "security edits their comment")

	// Reactions need comment access.
	code, _ = call(t, h, other, "POST", "/api/v1/comments/"+firstComment+"/reactions", map[string]any{"emoji": "👍"})
	mustStatus(t, code, 403, "view-only reacts")
	code, th = call(t, h, security, "POST", "/api/v1/comments/"+firstComment+"/reactions", map[string]any{"emoji": "👍"})
	mustStatus(t, code, 200, "security reacts")
	if re := list(list(th, "comments")[0], "reactions"); len(re) != 1 || str(re[0], "emoji") != "👍" {
		t.Fatalf("reactions = %v", re)
	}
	code, th = call(t, h, security, "DELETE", "/api/v1/comments/"+firstComment+"/reactions", map[string]any{"emoji": "👍"})
	mustStatus(t, code, 200, "security removes the reaction")
	if re := list(list(th, "comments")[0], "reactions"); len(re) != 0 {
		t.Fatalf("reaction should be gone, got %v", re)
	}

	// Resolve and reopen with comment access.
	code, th = call(t, h, security, "POST", "/api/v1/threads/"+threadID+"/resolve", nil)
	mustStatus(t, code, 200, "security resolves")
	if th["resolved"] != true {
		t.Fatalf("thread should be resolved: %v", th)
	}
	code, th = call(t, h, security, "POST", "/api/v1/threads/"+threadID+"/unresolve", nil)
	mustStatus(t, code, 200, "security reopens")
	if th["resolved"] != false {
		t.Fatalf("thread should be open again: %v", th)
	}

	// Deleting a comment: the author, or edit access. Soft delete keeps
	// the row without its body.
	code, _ = call(t, h, security, "DELETE", "/api/v1/comments/"+editorComment, nil)
	mustStatus(t, code, 403, "security deletes the editor's comment")
	code, th = call(t, h, editor, "DELETE", "/api/v1/comments/"+firstComment, nil)
	mustStatus(t, code, 200, "editor deletes security's comment")
	comments = list(th, "comments")
	if len(comments) != 2 || str(comments[0], "deleted_at") == "" || comments[0]["body"] != nil {
		t.Fatalf("soft delete should keep the row and drop the body, got %v", comments[0])
	}

	// Deleting the thread needs edit access.
	code, _ = call(t, h, security, "DELETE", "/api/v1/threads/"+threadID, nil)
	mustStatus(t, code, 403, "comment level deletes the thread")
	code, _ = call(t, h, editor, "DELETE", "/api/v1/threads/"+threadID, nil)
	mustStatus(t, code, 204, "editor deletes the thread")
	code, bodyOut = call(t, h, editor, "GET", "/api/v1/pages/"+page+"/threads", nil)
	mustStatus(t, code, 200, "threads after delete")
	if len(list(bodyOut, "threads")) != 0 {
		t.Fatalf("no threads should be left, got %v", bodyOut)
	}

	// No access reads as 404, never 403.
	code, _ = call(t, h, identity{user: "stranger", groups: ""}, "GET", "/api/v1/pages/"+page+"/threads", nil)
	mustStatus(t, code, 404, "a stranger reads threads")
}
