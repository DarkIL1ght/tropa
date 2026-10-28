// Чат встречи: новые сообщения приходят по SSE (/m/{token}/stream),
// отправка — fetch той же формы, что работает и без JS. Текст сообщений
// вставляется только через textContent: он не должен стать разметкой.
(function () {
  "use strict";

  var box = document.getElementById("chat");
  if (!box || !window.EventSource) return;
  var list = box.querySelector(".messages");
  var empty = box.querySelector(".chat-empty");
  var me = box.getAttribute("data-me");

  var lastId = 0;
  list.querySelectorAll("[data-id]").forEach(function (li) {
    lastId = Math.max(lastId, Number(li.getAttribute("data-id")));
  });
  scrollDown(true);

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  function nearBottom() {
    return list.scrollHeight - list.scrollTop - list.clientHeight < 80;
  }

  function scrollDown(force) {
    if (force || nearBottom()) list.scrollTop = list.scrollHeight;
  }

  function add(m) {
    if (m.id <= lastId || list.querySelector('[data-id="' + m.id + '"]')) return;
    lastId = m.id;
    var stick = nearBottom();
    var li = el("li", "msg" + (String(m.user_id) === me ? " mine" : ""));
    li.setAttribute("data-id", m.id);
    var head = el("span", "msg-head");
    var who = el("a", null, m.name);
    who.href = "/u/" + m.user_id;
    head.appendChild(who);
    head.appendChild(document.createTextNode(" "));
    head.appendChild(el("time", null, m.at));
    li.appendChild(head);
    li.appendChild(el("span", "msg-body", m.body));
    list.appendChild(li);
    if (empty) empty.hidden = true;
    scrollDown(stick);
  }

  // EventSource сам переподключается и шлёт Last-Event-ID; ?after — для первого раза
  var es = new EventSource(box.getAttribute("data-stream") + "?after=" + lastId);
  es.onmessage = function (e) {
    try { add(JSON.parse(e.data)); } catch (err) { /* битое событие пропускаем */ }
  };

  var form = box.querySelector(".chat-form");
  if (!form) return;
  var text = form.querySelector("textarea");
  var button = form.querySelector("button");
  var error = form.querySelector(".chat-error");

  function showError(msg) {
    error.textContent = msg;
    error.hidden = !msg;
  }

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    if (!text.value.trim()) return;
    button.disabled = true;
    showError("");
    fetch(form.action, {
      method: "POST",
      body: new FormData(form),
      headers: { "Accept": "application/json" },
      credentials: "same-origin"
    }).then(function (resp) {
      if (resp.ok) {
        text.value = "";
        return;
      }
      return resp.json().then(function (j) { showError(j.error || "Не отправилось — попробуйте ещё раз."); },
        function () { showError("Не отправилось — попробуйте ещё раз."); });
    }, function () {
      showError("Нет связи — сообщение не отправлено.");
    }).then(function () {
      button.disabled = false;
      text.focus();
    });
  });

  // Enter — отправить, Shift+Enter — новая строка
  text.addEventListener("keydown", function (e) {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      form.requestSubmit();
    }
  });
})();
