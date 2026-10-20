package web

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sgracia13/Cowboy/internal/app"
	"github.com/sgracia13/Cowboy/internal/forge"
	"github.com/sgracia13/Cowboy/internal/score"
	"github.com/sgracia13/Cowboy/internal/store"
)

func corralMux(t *testing.T) (*http.ServeMux, *app.App) {
	td := "../../testdata"
	f := forge.NewFake(filepath.Join(td, "prs"), filepath.Join(td, "acme"))
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.json"))
	a := app.New(f, st, map[string]string{"acme/app": filepath.Join(td, "acme")})
	for _, n := range f.All() {
		a.HandlePR("acme/app", n)
	}
	mux := http.NewServeMux()
	New(a).Routes(mux)
	return mux, a
}

// The corral marks every pen as a drop target and every card as draggable,
// with what the move needs, and loads the drag script.
func TestCorralIsDraggable(t *testing.T) {
	mux, _ := corralMux(t)
	body := checkPage(t, mux, "/", `data-pen="easy"`, `data-pen="normal"`, `data-pen="careful"`,
		`draggable="true"`, `data-repo="acme/app"`, `/static/drag.js`, `id="move-undo"`, "Drag a card to another pen")
	if strings.Count(body, `draggable="true"`) != strings.Count(body, `class="card"`) {
		t.Error("every card should be draggable")
	}
	w := do(mux, "GET", "/static/drag.js", nil, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "X-Cowboy-Move") {
		t.Errorf("drag.js: %d", w.Code)
	}
}

// A dragged move is the same correction as the buttons, answered with 204
// instead of a redirect; the buttons still redirect.
func TestMoveFromDrag(t *testing.T) {
	mux, a := corralMux(t)
	d, _ := a.Store.Get("acme/app", 506)
	to := score.Careful
	if d.CurrentPen() == score.Careful {
		to = score.Easy
	}
	before := len(a.Store.Corrections())
	w := do(mux, "POST", "/pr/move", url.Values{"repo": {"acme/app"}, "n": {"506"}, "to": {string(to)}}, map[string]string{"X-Cowboy-Move": "1"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("drag move: %d %s", w.Code, w.Body.String())
	}
	if d, _ := a.Store.Get("acme/app", 506); d.CurrentPen() != to || len(a.Store.Corrections()) != before+1 {
		t.Fatalf("not moved: pen %s, corrections %d", d.CurrentPen(), len(a.Store.Corrections()))
	}
	back := score.Normal
	w = do(mux, "POST", "/pr/move", url.Values{"repo": {"acme/app"}, "n": {"506"}, "to": {string(back)}}, nil)
	if w.Code != http.StatusSeeOther {
		t.Errorf("the buttons should still redirect: %d", w.Code)
	}
	if w := do(mux, "POST", "/pr/move", url.Values{"repo": {"acme/app"}, "n": {"506"}, "to": {"sideways"}}, map[string]string{"X-Cowboy-Move": "1"}); w.Code != 400 {
		t.Errorf("a bad pen should be refused: %d", w.Code)
	}
}
