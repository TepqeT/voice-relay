// Site motion: reveals blocks as they scroll into view (which also starts
// their CSS animations), counts the figures up, fills the guide's reading
// progress bar and highlights its current step. Without this script every
// page shows everything at once, unanimated. Visitors who ask for less
// motion get the final state, SVG animations paused.
(function () {
  var calm = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  if (calm) {
    document.querySelectorAll("svg").forEach(function (s) {
      if (s.pauseAnimations) s.pauseAnimations();
    });
    return;
  }
  if (!("IntersectionObserver" in window)) return;
  document.documentElement.classList.add("js");

  function countUp(el) {
    var to = parseInt(el.getAttribute("data-to"), 10) || 0;
    var start = null;
    function step(t) {
      if (start === null) start = t;
      var k = Math.min(1, (t - start) / 1400);
      el.textContent = String(Math.round(to * (1 - Math.pow(1 - k, 3))));
      if (k < 1) window.requestAnimationFrame(step);
    }
    el.textContent = "0";
    window.requestAnimationFrame(step);
  }

  var shown = new IntersectionObserver(function (entries) {
    entries.forEach(function (e) {
      if (!e.isIntersecting) return;
      e.target.classList.add("in");
      e.target.querySelectorAll(".count[data-to]").forEach(countUp);
      shown.unobserve(e.target);
    });
  }, { rootMargin: "0px 0px -10% 0px", threshold: 0.12 });
  document.querySelectorAll(".reveal").forEach(function (el) { shown.observe(el); });

  var links = document.querySelectorAll(".g-toc a");
  if (links.length) {
    var current = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (!e.isIntersecting) return;
        var passed = true;
        links.forEach(function (a) {
          var here = a.getAttribute("data-step") === e.target.id;
          a.classList.toggle("active", here);
          if (here) {
            passed = false;
            var toc = a.parentNode;
            toc.scrollLeft = a.offsetLeft - (toc.clientWidth - a.offsetWidth) / 2;
          }
          a.classList.toggle("done", passed && !here);
        });
      });
    }, { rootMargin: "-45% 0px -50% 0px" });
    document.querySelectorAll(".g-step").forEach(function (s) { current.observe(s); });
  }

  var bar = document.getElementById("g-bar");
  if (bar) {
    var ticking = false;
    var progress = function () {
      var max = document.documentElement.scrollHeight - window.innerHeight;
      bar.style.transform = "scaleX(" + (max > 0 ? Math.min(1, window.scrollY / max) : 0) + ")";
      ticking = false;
    };
    window.addEventListener("scroll", function () {
      if (!ticking) { ticking = true; window.requestAnimationFrame(progress); }
    }, { passive: true });
    progress();
  }
})();
