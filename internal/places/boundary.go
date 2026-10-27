package places

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// boundary.json — упрощённые границы Алматинской области (relation/215718)
// и города Алматы (relation/2465058) из OpenStreetMap. Рамки BBox слишком
// грубые: в прямоугольник области попадают Бишкек и Жетысуская область.
//
//go:embed boundary.json
var boundaryJSON []byte

// polygon — внешний контур и дыры; точки [lon, lat], как в GeoJSON.
type polygon [][][2]float64

type geometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
	polys       []polygon
}

var region, city geometry

func init() {
	var b struct {
		Region geometry `json:"region"`
		City   geometry `json:"city"`
	}
	if err := json.Unmarshal(boundaryJSON, &b); err != nil {
		panic(fmt.Sprintf("boundary.json: %v", err))
	}
	for _, g := range []*geometry{&b.Region, &b.City} {
		switch g.Type {
		case "Polygon":
			var p polygon
			if err := json.Unmarshal(g.Coordinates, &p); err != nil {
				panic(err)
			}
			g.polys = []polygon{p}
		case "MultiPolygon":
			if err := json.Unmarshal(g.Coordinates, &g.polys); err != nil {
				panic(err)
			}
		default:
			panic("boundary.json: тип " + g.Type)
		}
	}
	region, city = b.Region, b.City
}

// inRing — луч вправо от точки: нечётное число пересечений — внутри.
func inRing(lat, lon float64, ring [][2]float64) bool {
	inside := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		x1, y1 := ring[j][0], ring[j][1]
		x2, y2 := ring[i][0], ring[i][1]
		if (y1 > lat) != (y2 > lat) && lon < (x2-x1)*(lat-y1)/(y2-y1)+x1 {
			inside = !inside
		}
	}
	return inside
}

func (g *geometry) contains(lat, lon float64) bool {
	for _, p := range g.polys {
		if len(p) == 0 || !inRing(lat, lon, p[0]) {
			continue
		}
		hole := false
		for _, h := range p[1:] {
			if inRing(lat, lon, h) {
				hole = true
				break
			}
		}
		if !hole {
			return true
		}
	}
	return false
}

// InCity — точка в границах города Алматы.
func InCity(lat, lon float64) bool {
	return CityBBox.Contains(lat, lon) && city.contains(lat, lon)
}

// InArea — точка в Алматы или Алматинской области (город — отдельная
// административная единица, в полигоне области он «дыра»).
func InArea(lat, lon float64) bool {
	if !RegionBBox.Contains(lat, lon) {
		return false
	}
	return InCity(lat, lon) || region.contains(lat, lon)
}

// AreaOf — almaty, region или "" для точки вне обеих границ.
func AreaOf(lat, lon float64) Area {
	switch {
	case InCity(lat, lon):
		return Almaty
	case InArea(lat, lon):
		return Region
	default:
		return ""
	}
}
