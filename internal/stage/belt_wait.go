package stage

import (
	"time"

	"factorio-bot/internal/layout"
)

// beltSpeed — тайлов в секунду у жёлтой ленты (до стадии 68 ленты — первого уровня).
const beltSpeed = 1.875

// beltWait — сколько ждать, пока предмет пройдёт ленты раскладки list: не меньше least, а лент
// длинных — их длина на скорость жёлтой ленты с запасом в полтора раза и минутой (подземка — до
// 6 тайлов пролёта на пару, считается за 3 на штуку). Было 3 минуты на стадии 16 (778631482:
// лента угля станции — около 400 тайлов, уголь дошёл до бойлера через 3.5 минуты, стадия — на
// повтор).
func beltWait(list []layout.Placement, least time.Duration) time.Duration {
	tiles := 0
	for _, pl := range list {
		switch pl.Name {
		case "transport-belt":
			tiles++
		case "underground-belt":
			tiles += 3
		}
	}
	return max(least, time.Duration(float64(tiles)/beltSpeed*1.5*float64(time.Second))+time.Minute)
}
