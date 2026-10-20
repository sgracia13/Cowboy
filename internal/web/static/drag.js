// Drag a card to another pen on the corral to move it. The move is saved
// after a few seconds, so Undo puts it back without counting as a move;
// leaving the page saves it straight away. The move buttons on each PR's
// page do the same thing, for keyboards and touch screens.
(function () {
  var live = document.getElementById("live");
  var toast = document.getElementById("move-toast");
  var text = document.getElementById("move-text");
  var undo = document.getElementById("move-undo");
  if (!live || !toast) return;
  var WAIT = 5000;
  var drag = null;    // {card, from}
  var waiting = null; // {card, from, to, timer, prev, next}

  function penOf(el) { return el && el.closest ? el.closest("[data-pen]") : null; }
  function counts() {
    live.querySelectorAll("[data-pen]").forEach(function (pen) {
      var c = pen.querySelector(".count");
      var n = pen.querySelectorAll(".card").length;
      if (c) c.textContent = n;
      var empty = pen.querySelector(".empty");
      if (empty) empty.hidden = n > 0;
    });
  }
  function hold(on) {
    if (on) document.body.setAttribute("data-hold", "1");
    else { document.body.removeAttribute("data-hold"); if (window.cowboyRefresh) window.cowboyRefresh(); }
  }
  function save(w, leaving) {
    clearTimeout(w.timer);
    var body = new URLSearchParams({ repo: w.card.dataset.repo, n: w.card.dataset.n, to: w.to });
    return fetch("/pr/move", { method: "POST", body: body, keepalive: !!leaving, credentials: "same-origin",
      headers: { "X-Cowboy-Move": "1" } }).then(function (r) {
      if (!r.ok) throw r.status;
    });
  }
  function settle() {
    var w = waiting;
    if (!w) return;
    waiting = null;
    toast.hidden = true;
    save(w).catch(function () {
      // put it back and say so
      w.from.insertBefore(w.card, w.next && w.next.parentNode === w.from ? w.next : null);
      counts();
      text.textContent = "Couldn't move #" + w.card.dataset.n + ". It's back where it was.";
      undo.hidden = true;
      toast.hidden = false;
      setTimeout(function () { toast.hidden = true; undo.hidden = false; }, 4000);
    }).then(function () { hold(false); });
  }

  // the card under the pointer when the button went down: a fast flick can
  // start the drag over the next card, so that one is the one moved
  var downOn = null;
  live.addEventListener("pointerdown", function (e) {
    downOn = e.target.closest ? e.target.closest(".card") : null;
  });
  live.addEventListener("dragstart", function (e) {
    var card = downOn || (e.target.closest && e.target.closest(".card"));
    downOn = null;
    if (!card) return;
    if (waiting) settle(); // one move at a time
    drag = { card: card, from: penOf(card) };
    card.classList.add("dragging");
    e.dataTransfer.effectAllowed = "move";
    e.dataTransfer.setData("text/plain", card.dataset.pr);
  });
  live.addEventListener("dragend", function () {
    if (drag) drag.card.classList.remove("dragging");
    live.querySelectorAll(".drop-target").forEach(function (p) { p.classList.remove("drop-target"); });
    drag = null;
  });
  live.addEventListener("dragover", function (e) {
    var pen = penOf(e.target);
    if (!drag || !pen || pen === drag.from) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = "move";
    live.querySelectorAll(".drop-target").forEach(function (p) { if (p !== pen) p.classList.remove("drop-target"); });
    pen.classList.add("drop-target");
  });
  live.addEventListener("dragleave", function (e) {
    var pen = penOf(e.target);
    if (pen && !pen.contains(e.relatedTarget)) pen.classList.remove("drop-target");
  });
  live.addEventListener("drop", function (e) {
    var pen = penOf(e.target);
    if (!drag || !pen || pen === drag.from) return;
    e.preventDefault();
    pen.classList.remove("drop-target");
    var card = drag.card, from = drag.from, next = card.nextElementSibling;
    var head = pen.querySelector(".pen-sub");
    pen.insertBefore(card, head ? head.nextSibling : pen.firstChild);
    card.classList.remove("dragging");
    card.classList.add("fresh");
    counts();
    hold(true);
    waiting = { card: card, from: from, to: pen.dataset.pen, next: next, timer: setTimeout(settle, WAIT) };
    text.textContent = "Moved #" + card.dataset.n + " to " + pen.dataset.pen + ".";
    undo.hidden = false;
    toast.hidden = false;
    drag = null;
  });
  undo.addEventListener("click", function () {
    var w = waiting;
    if (!w) return;
    clearTimeout(w.timer);
    waiting = null;
    w.from.insertBefore(w.card, w.next && w.next.parentNode === w.from ? w.next : null);
    counts();
    toast.hidden = true;
    hold(false);
  });
  window.addEventListener("pagehide", function () {
    if (waiting) { var w = waiting; waiting = null; save(w, true).catch(function () {}); }
  });
})();
