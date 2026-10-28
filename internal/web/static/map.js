// Карта мест (главная) и мини-карта на странице места.
// Данные — /api/places (GeoJSON). Текст из данных вставляется только через
// textContent: названия мест не должны превращаться в разметку.
(function () {
  "use strict";

  var ALMATY = [43.238, 76.925];
  // Алматинская область с запасом — дальше карту не утащить.
  var REGION = L.latLngBounds([41.6, 73.5], [47.3, 81.1]);
  var TILES = "https://tile.openstreetmap.org/{z}/{x}/{y}.png";
  var ATTRIBUTION = '© <a href="https://www.openstreetmap.org/copyright">участники OpenStreetMap</a>';

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  function pinIcon(category) {
    return L.divIcon({
      className: "pin-wrap",
      html: '<span class="pin cat-' + category.replace(/[^a-z]/g, "") + '"></span>',
      iconSize: [22, 22],
      iconAnchor: [11, 11],
      popupAnchor: [0, -10],
    });
  }

  function baseMap(node, opts) {
    var map = L.map(node, Object.assign({ maxBounds: REGION, maxBoundsViscosity: 0.8, minZoom: 7 }, opts));
    L.tileLayer(TILES, { maxZoom: 19, attribution: ATTRIBUTION }).addTo(map);
    map.attributionControl.setPrefix(false);
    return map;
  }

  // «1 место», «3 места», «5 мест»
  function plural(n, one, few, many) {
    var n10 = n % 10, n100 = n % 100;
    if (n10 === 1 && n100 !== 11) return one;
    if (n10 >= 2 && n10 <= 4 && (n100 < 12 || n100 > 14)) return few;
    return many;
  }

  // --- мини-карта на странице места ---
  var mini = document.getElementById("place-map");
  if (mini) {
    var ll = [parseFloat(mini.dataset.lat), parseFloat(mini.dataset.lon)];
    var m = baseMap(mini, { zoomControl: true, scrollWheelZoom: false });
    m.setView(ll, 14);
    L.marker(ll, { icon: pinIcon(mini.dataset.category || "") }).addTo(m);
    return;
  }

  var node = document.getElementById("map");
  if (!node) return;

  // --- главная карта ---
  var state = { cats: new Set(), indoor: false, free: false, q: "" };
  var params = new URLSearchParams(location.search);
  (params.get("cat") || "").split(",").filter(Boolean).forEach(function (c) { state.cats.add(c); });
  state.indoor = params.get("indoor") === "1";
  state.free = params.get("free") === "1";
  state.q = params.get("q") || "";

  var map = baseMap(node, { zoomControl: false });
  L.control.zoom({ position: "bottomright" }).addTo(map);
  map.setView(ALMATY, 12);

  var cluster = L.markerClusterGroup({ showCoverageOnHover: false, maxClusterRadius: 45 });
  map.addLayer(cluster);

  var list = document.getElementById("place-list");
  var count = document.getElementById("count");
  var qInput = document.getElementById("q");
  var indoorBox = document.getElementById("indoor");
  var freeBox = document.getElementById("free");
  var chips = document.querySelectorAll(".chip[data-cat]");
  var panel = document.getElementById("panel");
  var toggle = document.getElementById("panel-toggle");

  qInput.value = state.q;
  indoorBox.checked = state.indoor;
  freeBox.checked = state.free;
  chips.forEach(function (b) { b.setAttribute("aria-pressed", String(state.cats.has(b.dataset.cat))); });

  function syncURL() {
    var p = new URLSearchParams();
    if (state.cats.size) p.set("cat", Array.from(state.cats).join(","));
    if (state.indoor) p.set("indoor", "1");
    if (state.free) p.set("free", "1");
    if (state.q) p.set("q", state.q);
    var qs = p.toString();
    history.replaceState(null, "", qs ? "?" + qs : location.pathname);
  }

  function popupFor(props) {
    var box = el("div", "popup");
    box.appendChild(el("strong", null, props.name));
    var meta = [props.kind];
    if (props.indoor) meta.push("в помещении");
    if (props.free) meta.push("бесплатно");
    if (props.season) meta.push(props.season.toLowerCase());
    if (props.off_season) meta.push("сейчас не сезон");
    box.appendChild(el("div", "muted", meta.join(" · ")));
    if (props.meetups) {
      box.appendChild(el("div", "popup-meetups", props.meetups + " " + plural(props.meetups, "встреча", "встречи", "встреч")));
    }
    var a = el("a", null, props.meetups ? "Встречи и подробнее →" : "Подробнее →");
    a.href = props.url;
    box.appendChild(a);
    var make = el("a", null, "Собрать встречу");
    make.href = props.url + "/meetups/new";
    box.appendChild(make);
    return box;
  }

  var markers = {};
  var controller = null;
  var fitNext = false;

  function render(fc) {
    cluster.clearLayers();
    markers = {};
    list.replaceChildren();
    var center = map.getCenter();
    var feats = fc.features.slice().sort(function (a, b) {
      var da = center.distanceTo([a.geometry.coordinates[1], a.geometry.coordinates[0]]);
      var db = center.distanceTo([b.geometry.coordinates[1], b.geometry.coordinates[0]]);
      return da - db;
    });
    var layers = feats.map(function (f) {
      var p = f.properties;
      var ll = [f.geometry.coordinates[1], f.geometry.coordinates[0]];
      var mk = L.marker(ll, { icon: pinIcon(p.category), title: p.name });
      mk.bindPopup(function () { return popupFor(p); });
      markers[p.slug] = mk;

      var li = el("li");
      var btn = el("button", "place-item");
      btn.type = "button";
      btn.appendChild(el("span", "pin cat-" + p.category.replace(/[^a-z]/g, "")));
      var text = el("span", "place-text");
      text.appendChild(el("span", "place-name", p.name));
      text.appendChild(el("span", "muted", p.kind + (p.indoor ? " · в помещении" : "") + (p.free ? " · бесплатно" : "") +
        (p.off_season ? " · не сезон" : "") +
        (p.meetups ? " · " + p.meetups + " " + plural(p.meetups, "встреча", "встречи", "встреч") : "")));
      btn.appendChild(text);
      btn.addEventListener("click", function () {
        closePanel();
        cluster.zoomToShowLayer(mk, function () { mk.openPopup(); });
      });
      li.appendChild(btn);
      list.appendChild(li);
      return mk;
    });
    cluster.addLayers(layers);

    var n = feats.length;
    count.textContent = n
      ? (state.q ? "Найдено: " : "На карте: ") + n + " " + plural(n, "место", "места", "мест")
      : state.q ? "Ничего не нашли. Попробуйте другое слово." : "Здесь мест нет — сдвиньте карту или снимите фильтры.";

    if (fitNext && n) {
      map.fitBounds(cluster.getBounds(), { padding: [40, 40], maxZoom: 13 });
    }
    fitNext = false;
  }

  var suppressMove = false;
  function load() {
    if (controller) controller.abort();
    controller = new AbortController();
    var p = new URLSearchParams();
    // при поиске по тексту — по всей области, иначе — только видимая часть карты
    if (state.q) {
      p.set("q", state.q);
      fitNext = true;
    } else {
      var b = map.getBounds().pad(0.2);
      p.set("bbox", [b.getSouth(), b.getWest(), b.getNorth(), b.getEast()].map(function (x) { return x.toFixed(5); }).join(","));
    }
    if (state.cats.size) p.set("cat", Array.from(state.cats).join(","));
    if (state.indoor) p.set("indoor", "1");
    if (state.free) p.set("free", "1");
    fetch("/api/places?" + p.toString(), { signal: controller.signal })
      .then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.json();
      })
      .then(function (fc) {
        if (fitNext) suppressMove = true;
        render(fc);
      })
      .catch(function (err) {
        if (err.name === "AbortError") return;
        count.textContent = "Не удалось загрузить места. Обновите страницу.";
      });
  }

  var timer = null;
  function schedule(delay) {
    clearTimeout(timer);
    timer = setTimeout(load, delay);
  }

  map.on("moveend", function () {
    if (suppressMove) { suppressMove = false; return; }
    if (!state.q) schedule(200);
  });

  chips.forEach(function (b) {
    b.addEventListener("click", function () {
      var c = b.dataset.cat;
      if (state.cats.has(c)) state.cats.delete(c); else state.cats.add(c);
      b.setAttribute("aria-pressed", String(state.cats.has(c)));
      syncURL();
      schedule(0);
    });
  });
  indoorBox.addEventListener("change", function () { state.indoor = indoorBox.checked; syncURL(); schedule(0); });
  freeBox.addEventListener("change", function () { state.free = freeBox.checked; syncURL(); schedule(0); });
  qInput.addEventListener("input", function () { state.q = qInput.value.trim(); syncURL(); schedule(300); });
  // Плашка непогоды: оставляет на карте только места в тепле.
  var weatherGo = document.getElementById("weather-go");
  if (weatherGo) {
    weatherGo.addEventListener("click", function () {
      state.cats = new Set([weatherGo.dataset.cat]);
      chips.forEach(function (b) { b.setAttribute("aria-pressed", String(state.cats.has(b.dataset.cat))); });
      syncURL();
      schedule(0);
    });
  }
  document.getElementById("filters").addEventListener("submit", function (e) { e.preventDefault(); schedule(0); });

  function closePanel() {
    panel.classList.remove("open");
    toggle.setAttribute("aria-expanded", "false");
    toggle.textContent = "Список мест";
  }
  toggle.addEventListener("click", function () {
    var open = panel.classList.toggle("open");
    toggle.setAttribute("aria-expanded", String(open));
    toggle.textContent = open ? "Карта" : "Список мест";
  });

  load();
})();
