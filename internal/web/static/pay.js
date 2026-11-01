// PayPal Checkout on the site: the server creates and captures the order,
// this script only shows PayPal's buttons and relays their events.
(function () {
  var box = document.getElementById("checkout");
  if (!box || !window.paypal) return;
  var msg = document.getElementById("pay-msg");
  var fallback = box.getAttribute("data-error");

  function plan() {
    var c = box.querySelector("input[name=plan]:checked");
    return c ? c.value : "";
  }

  function post(url, fields) {
    var body = new URLSearchParams(fields);
    body.set("csrf", box.getAttribute("data-csrf"));
    return fetch(url, {
      method: "POST",
      body: body,
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
    style: { layout: "vertical", shape: "rect", label: "pay" },
    createOrder: function () {
      msg.textContent = "";
      return post("/pay/order", { plan: plan() }).then(function (j) { return j.id; });
    },
    onApprove: function (data) {
      return post("/pay/capture", { order: data.orderID }).then(function (j) {
        window.location.href = j.redirect;
      });
    },
    onError: function (err) {
      msg.textContent = (err && err.message) || fallback;
    }
  }).render("#paypal-buttons");
})();
