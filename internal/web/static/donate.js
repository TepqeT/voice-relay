// PayPal donations on the site: the server creates and captures the order,
// this script only shows PayPal's buttons, relays their events and thanks.
(function () {
  var box = document.getElementById("checkout");
  if (!box || !window.paypal) return;
  var msg = document.getElementById("pay-msg");
  var fallback = box.getAttribute("data-error");

  function amount() {
    var c = box.querySelector("input[name=amount]:checked");
    return c ? c.value : "";
  }

  function say(text, ok) {
    msg.textContent = text;
    msg.className = "pay-msg " + (ok ? "ok" : "bad");
  }

  function post(url, fields) {
    return fetch(url, {
      method: "POST",
      body: new URLSearchParams(fields),
      credentials: "same-origin",
      headers: { "Content-Type": "application/x-www-form-urlencoded" }
    }).then(function (r) {
      return r.json().then(function (j) {
        if (!r.ok) throw new Error(j.error || fallback);
        return j;
      });
    });
  }

  window.paypal.Buttons({
    style: { layout: "vertical", shape: "pill", label: "donate" },
    createOrder: function () {
      say("", true);
      return post("/donate/order", { amount: amount() }).then(function (j) { return j.id; });
    },
    onApprove: function (data) {
      return post("/donate/capture", { order: data.orderID }).then(function (j) {
        say(j.thanks, true);
        box.classList.add("thanked");
      });
    },
    onError: function (err) {
      say((err && err.message) || fallback, false);
    }
  }).render("#paypal-buttons");
})();
