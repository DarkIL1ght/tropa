// Карта в форме «Предложить место»: клик ставит точку и пишет координаты
// в поле coords, ввод координат вручную двигает точку. Без JS форма
// работает — координаты вводятся текстом.
(function () {
  "use strict";

  var node = document.getElementById("pick-map");
  var input = document.getElementById("coords");
  if (!node || !input) return;

  var ALMATY = [43.238, 76.925];
  var REGION = L.latLngBounds([41.6, 73.5], [47.3, 81.1]);

  var map = L.map(node, { maxBounds: REGION, maxBoundsViscosity: 0.8, minZoom: 7, scrollWheelZoom: false });
  L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: '© <a href="https://www.openstreetmap.org/copyright">участники OpenStreetMap</a>',
  }).addTo(map);
  map.attributionControl.setPrefix(false);

  var icon = L.divIcon({ className: "pin-wrap", html: '<span class="pin pick"></span>', iconSize: [26, 26], iconAnchor: [13, 13] });
  var marker = null;

  function parse(s) {
    var parts = s.split(/[\s,;]+/).filter(Boolean);
    if (parts.length !== 2) return null;
    var lat = parseFloat(parts[0]), lon = parseFloat(parts[1]);
    if (!isFinite(lat) || !isFinite(lon) || Math.abs(lat) > 90 || Math.abs(lon) > 180) return null;
    return L.latLng(lat, lon);
  }

  function place(ll, pan) {
    if (!marker) {
      marker = L.marker(ll, { icon: icon, draggable: true, keyboard: false }).addTo(map);
      marker.on("dragend", function () { write(marker.getLatLng()); });
    } else {
      marker.setLatLng(ll);
    }
    if (pan) map.setView(ll, Math.max(map.getZoom(), 15));
  }

  function write(ll) {
    input.value = ll.lat.toFixed(6) + ", " + ll.lng.toFixed(6);
  }

  var start = parse(input.value);
  if (start) {
    map.setView(start, 15);
    place(start, false);
  } else {
    map.setView(ALMATY, 12);
  }

  map.on("click", function (e) {
    place(e.latlng, false);
    write(e.latlng);
  });
  input.addEventListener("change", function () {
    var ll = parse(input.value);
    if (ll) place(ll, true);
  });
})();
