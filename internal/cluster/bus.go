package cluster

// Шина — зона шины (zones.md): ленты предметов в один ряд поперёк (v), все текут в одну сторону.
// Поперёк: пары лент через зазоры в 3 тайла (в зазоре — половина делителя, поворот и тайл между
// подземками; v 0–2 зазор, 3–4 ленты, 5–7 зазор, …).

const Bus Kind = "bus"

// laneV — ряд j-й ленты: пары через зазоры в 3.
func laneV(j int) int { return 3 + (j/2)*5 + j%2 }

// busWidth — ширина ряда лент на n лент (с зазорами по краям).
func busWidth(n int) int { return 5*((n+1)/2) + 3 }

// laneRow — ряд v — лента (или место будущей), не зазор.
func laneRow(v, width int) bool { return v >= 3 && v < width && (v-3)%5 < 2 }

func (c *Cluster) frame() frame {
	return frame{r: c.Area, vertical: c.Vertical, up: (c.Upright || c.Back) && c.Vertical, back: c.Back && !c.Vertical}
}
