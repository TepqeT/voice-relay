// Relay bot invites: each opens in a new tab. When the administrator comes
// back to this tab, the page reloads so the next missing bot is offered.
(function () {
  var link = document.getElementById("next-relay");
  if (!link) return;
  var left = false;
  link.addEventListener("click", function () { left = true; });
  document.addEventListener("visibilitychange", function () {
    if (left && document.visibilityState === "visible") {
      setTimeout(function () { location.reload(); }, 1500);
    }
  });
  window.addEventListener("focus", function () {
    if (left) setTimeout(function () { location.reload(); }, 1500);
  });
})();
