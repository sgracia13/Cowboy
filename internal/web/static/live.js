// cowboy live updates: listens to /events and redraws the pens in place.
(function () {
  // set on PR pages as data-pr on this script tag (no inline scripts, so a strict CSP works)
  var PR = (document.currentScript && document.currentScript.getAttribute("data-pr")) || "";
  var dot = document.getElementById("live-dot");
  var tickerText = document.getElementById("ticker-text");
  var live = document.getElementById("live");
  var es;
  var pending = false, again = false, fresh = [];

  function connect() {
    es = new EventSource("/events");
    es.onopen = function () { if (dot) dot.classList.add("on"); };
    es.onerror = function () { if (dot) dot.classList.remove("on"); };
    es.onmessage = function (m) {
      var e; try { e = JSON.parse(m.data); } catch (_) { return; }
      var key = e.repo + "#" + e.number;
      if (PR) {                          // on a PR page: reload when this PR changes
        if (key === PR) location.reload();
        return;
      }
      if (tickerText) {
        tickerText.textContent = e.text;
      }
      fresh.push(key);
      refresh();
    };
  }

  function refresh() {
    if (!live) return;
    if (pending) { again = true; return; }
    pending = true;
    fetch(location.pathname + location.search, { headers: { "X-Cowboy-Partial": "1" } })
      .then(function (r) { return r.ok ? r.text() : Promise.reject(r.status); })
      .then(function (html) {
        live.innerHTML = html;
        fresh.forEach(function (k) {
          var el = live.querySelector('[data-pr="' + k + '"]');
          if (el) { el.classList.add("fresh"); }
        });
        fresh = [];
      })
      .catch(function () {})
      .then(function () {
        pending = false;
        if (again) { again = false; refresh(); }
      });
  }

  if (window.EventSource) connect();
})();
