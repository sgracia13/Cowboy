// Copy buttons: <button data-copy="element-id">. The label says what happened.
document.addEventListener("click", function (e) {
  var b = e.target.closest("[data-copy]");
  if (!b) return;
  var el = document.getElementById(b.getAttribute("data-copy"));
  if (!el) return;
  var label = b.textContent;
  function done(ok) {
    b.textContent = ok ? "Copied" : "Select and copy";
    setTimeout(function () { b.textContent = label; }, 1800);
  }
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(el.textContent).then(function () { done(true); }, function () { done(false); });
  } else {
    var r = document.createRange(); r.selectNodeContents(el);
    var s = window.getSelection(); s.removeAllRanges(); s.addRange(r);
    done(document.execCommand && document.execCommand("copy"));
  }
});
