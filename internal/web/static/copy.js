// Кнопка «Скопировать» рядом с полем ссылки. Без JS поле остаётся
// обычным текстом, который можно выделить и скопировать руками.
(function () {
  "use strict";
  document.querySelectorAll(".copy-btn").forEach(function (btn) {
    var src = btn.parentNode.querySelector(".copy-src");
    if (!src || !navigator.clipboard) return;
    btn.hidden = false;
    btn.addEventListener("click", function () {
      navigator.clipboard.writeText(src.value).then(function () {
        btn.textContent = "Скопировано";
        setTimeout(function () { btn.textContent = "Скопировать"; }, 2000);
      }, function () {
        src.select();
      });
    });
  });
})();
