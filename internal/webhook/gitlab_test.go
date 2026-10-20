package webhook

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func glPost(h *Handler, token, uuid, body string) int {
	r := httptest.NewRequest("POST", "/webhook/gitlab", strings.NewReader(body))
	r.Header.Set("X-Gitlab-Token", token)
	if uuid != "" {
		r.Header.Set("X-Gitlab-Event-UUID", uuid)
	}
	w := httptest.NewRecorder()
	h.GitLab(w, r)
	return w.Code
}

func TestGitLabHooks(t *testing.T) {
	q := make(chan Job, 20)
	h := &Handler{Secret: "s3cret", Queue: q, Self: "cowboy-bot"}
	mr := func(user, action, changes string) string {
		return `{"object_kind":"merge_request","user":{"username":"` + user + `"},"project":{"path_with_namespace":"grp/sub/app"},"object_attributes":{"iid":7,"action":"` + action + `"}` + changes + `}`
	}
	next := func() *Job {
		select {
		case j := <-q:
			return &j
		default:
			return nil
		}
	}
	if c := glPost(h, "wrong", "", mr("ann", "open", "")); c != 401 {
		t.Fatalf("bad token: %d", c)
	}
	if c := glPost(h, "", "", mr("ann", "open", "")); c != 401 {
		t.Fatalf("missing token: %d", c)
	}
	if glPost(h, "s3cret", "u1", mr("ann", "open", "")); !eq(next(), Job{Repo: "grp/sub/app", Number: 7}) {
		t.Error("open should queue a sort")
	}
	glPost(h, "s3cret", "u1", mr("ann", "open", ""))
	if next() != nil {
		t.Error("a redelivery should be ignored")
	}
	// a person moves it to careful
	labels := `,"changes":{"labels":{"previous":[{"title":"cowboy:easy"}],"current":[{"title":"cowboy:careful"}]}}`
	if glPost(h, "s3cret", "u2", mr("ann", "update", labels)); !eq(next(), Job{Repo: "grp/sub/app", Number: 7, Repen: "careful", By: "ann"}) {
		t.Error("a person's label change should be a correction")
	}
	// Cowboy's own label change is not
	glPost(h, "s3cret", "u3", mr("cowboy-bot", "update", labels))
	if j := next(); j == nil || j.Repen != "" {
		t.Errorf("own label change: %+v", j)
	}
	// a project access token's bot isn't a person either
	glPost(h, "s3cret", "u4", mr("project_42_bot_abc", "update", labels))
	if j := next(); j == nil || j.Repen != "" {
		t.Errorf("bot label change: %+v", j)
	}
	unflag := `,"changes":{"labels":{"previous":[{"title":"agent:likely"}],"current":[]}}`
	if glPost(h, "s3cret", "u5", mr("ann", "update", unflag)); !eq(next(), Job{Repo: "grp/sub/app", Number: 7, Unflag: true, By: "ann"}) {
		t.Error("removing agent:likely should unflag")
	}
	if glPost(h, "s3cret", "u6", mr("ann", "merge", "")); !eq(next(), Job{Repo: "grp/sub/app", Number: 7, Closed: true, Merged: true}) {
		t.Error("merge")
	}
	if glPost(h, "s3cret", "u7", mr("ann", "close", "")); !eq(next(), Job{Repo: "grp/sub/app", Number: 7, Closed: true}) {
		t.Error("close")
	}
	pipe := `{"object_kind":"pipeline","project":{"path_with_namespace":"grp/sub/app"},"object_attributes":{"status":"failed"},"merge_request":{"iid":7}}`
	if glPost(h, "s3cret", "u8", pipe); !eq(next(), Job{Repo: "grp/sub/app", Number: 7}) {
		t.Error("a finished pipeline should re-sort its MR")
	}
	running := strings.Replace(pipe, "failed", "running", 1)
	if glPost(h, "s3cret", "u9", running); next() != nil {
		t.Error("a running pipeline shouldn't queue anything")
	}
}

func eq(j *Job, want Job) bool {
	return j != nil && j.Repo == want.Repo && j.Number == want.Number && j.Closed == want.Closed && j.Merged == want.Merged &&
		j.Repen == want.Repen && j.By == want.By && j.Unflag == want.Unflag
}
