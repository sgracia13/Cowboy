// Setup pages: send the manifest on to GitHub, then come back to the corral.
// (A separate file because the dashboard doesn't allow inline scripts.)
document.addEventListener("DOMContentLoaded", function () {
  var f = document.getElementById("to-github");
  if (f) f.submit();
  if (document.getElementById("go-corral")) setTimeout(function () { location.href = "/"; }, 2500);
});
