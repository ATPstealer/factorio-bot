// Package bridge — типизированные вызовы read-only интерфейса мода botbridge по RCON.
package bridge

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"factorio-bot/internal/rcon"
)

type Execer interface {
	Exec(cmd string) (string, error)
}

type Bridge struct {
	rc Execer
}

func New(rc Execer) *Bridge { return &Bridge{rc: rc} }

// Dial — RCON-подключение к серверу и Bridge поверх него.
func Dial(addr, password string) (*Bridge, *rcon.Client, error) {
	rc, err := rcon.Dial(addr, password, 0)
	if err != nil {
		return nil, nil, err
	}
	return New(rc), rc, nil
}

// Call вызывает метод мода и декодирует result в out (out может быть nil).
func (b *Bridge) Call(method string, args any, out any) error {
	req, err := json.Marshal(map[string]any{"method": method, "args": args})
	if err != nil {
		return err
	}
	res, err := b.rc.Exec(command(string(req)))
	if err != nil {
		return fmt.Errorf("bridge %s: %w", method, err)
	}
	var resp struct {
		OK     bool            `json:"ok"`
		Error  string          `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(res), &resp); err != nil {
		return fmt.Errorf("bridge %s: ответ %.200q: %w", method, res, err)
	}
	if !resp.OK {
		return fmt.Errorf("bridge %s: %s", method, resp.Error)
	}
	if out == nil || len(resp.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("bridge %s: %s: %w", method, resp.Result, err)
	}
	return nil
}

// command строит консольную команду; JSON передаётся Lua-строкой в длинных скобках,
// поэтому кавычки и обратные слэши не экранируются.
func command(req string) string {
	eq := "="
	for strings.Contains(req, "]"+eq+"]") {
		eq += "="
	}
	return `/sc remote.call("bot","call",[` + eq + `[` + req + `]` + eq + `])`
}

// Say пишет строку в консоль (чат) игры от имени <server>: обычный текст по RCON игра
// показывает как сообщение. Только отображение — на игру не влияет, мод и Lua не нужны.
func (b *Bridge) Say(text string) error {
	text = strings.Join(strings.Fields(text), " ") // одна строка
	if strings.HasPrefix(text, "/") {
		text = " " + text // иначе это была бы команда
	}
	_, err := b.rc.Exec(text)
	return err
}

// Типы ответов ------------------------------------------------------------

// List — массив из Lua: пустая таблица приходит из helpers.table_to_json как {}, а не [].
type List[T any] []T

func (l *List[T]) UnmarshalJSON(b []byte) error {
	if string(b) == "{}" {
		*l = nil
		return nil
	}
	return json.Unmarshal(b, (*[]T)(l))
}

type Pos struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Box — прямоугольник в тайлах (selection_box, bounding_box). Orientation (доля оборота
// по часовой, утёсы-углы — 0.125) — рамка повёрнута вокруг центра.
type Box struct {
	LeftTop     Pos     `json:"left_top"`
	RightBottom Pos     `json:"right_bottom"`
	Orientation float64 `json:"o,omitempty"`
}

func (b Box) Center() Pos {
	return Pos{X: (b.LeftTop.X + b.RightBottom.X) / 2, Y: (b.LeftTop.Y + b.RightBottom.Y) / 2}
}

// Contains — точка внутри рамки, раздутой на pad по её осям (с учётом поворота).
func (b Box) Contains(p Pos, pad float64) bool {
	c := b.Center()
	dx, dy := p.X-c.X, p.Y-c.Y
	if b.Orientation != 0 {
		a := b.Orientation * 2 * math.Pi
		dx, dy = dx*math.Cos(a)+dy*math.Sin(a), -dx*math.Sin(a)+dy*math.Cos(a)
	}
	return math.Abs(dx) < (b.RightBottom.X-b.LeftTop.X)/2+pad && math.Abs(dy) < (b.RightBottom.Y-b.LeftTop.Y)/2+pad
}

// Bounds — охватывающий прямоугольник без поворота.
func (b Box) Bounds() Box {
	if b.Orientation == 0 {
		return b
	}
	c := b.Center()
	hx, hy := (b.RightBottom.X-b.LeftTop.X)/2, (b.RightBottom.Y-b.LeftTop.Y)/2
	a := b.Orientation * 2 * math.Pi
	ex := math.Abs(hx*math.Cos(a)) + math.Abs(hy*math.Sin(a))
	ey := math.Abs(hx*math.Sin(a)) + math.Abs(hy*math.Cos(a))
	return Box{LeftTop: Pos{X: c.X - ex, Y: c.Y - ey}, RightBottom: Pos{X: c.X + ex, Y: c.Y + ey}}
}

type Entity struct {
	Name         string  `json:"name"`
	Type         string  `json:"type"`
	Position     Pos     `json:"position"`
	UnitNumber   int     `json:"unit_number,omitempty"`
	SelectionBox *Box    `json:"selection_box,omitempty"`
	Direction    *int    `json:"direction,omitempty"` // 2.0: 0 север, 4 восток, 8 юг, 12 запад
	Item         string  `json:"item,omitempty"`      // у предмета на земле (item-entity) — что лежит
	Distance     float64 `json:"distance,omitempty"`

	// только у find_minable
	Products   List[Product] `json:"products,omitempty"`
	MiningTime float64       `json:"mining_time,omitempty"`
	Amount     int           `json:"amount,omitempty"` // остаток руды у resource
}

type Product struct {
	Name   string  `json:"name"`
	Amount float64 `json:"amount"`
}

type Stack struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Slot — непустая ячейка основного инвентаря (номер с 1).
type Slot struct {
	Slot  int    `json:"slot"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Craft struct {
	Recipe       string `json:"recipe"`
	Count        int    `json:"count"`
	Prerequisite bool   `json:"prerequisite"` // промежуточный крафт, поставленный игрой автоматически
}

type Character struct {
	Reach         float64 `json:"reach"`
	ResourceReach float64 `json:"resource_reach"`
	Walking       bool    `json:"walking"`
	Mining        bool    `json:"mining"`
	Speed         float64 `json:"speed"`
}

type Player struct {
	Name       string  `json:"name"`
	Index      int     `json:"index"`
	Position   Pos     `json:"position"`
	Zoom       float64 `json:"zoom"`
	Resolution struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"resolution"`
	Scale             float64        `json:"scale"`
	Controller        int            `json:"controller"`
	ControllerName    string         `json:"controller_name"` // "character"; "cutscene" — заставка (Tab пропускает)
	Selected          *Entity        `json:"selected"`
	OpenedGUIType     *int           `json:"opened_gui_type"`
	OpenedGUI         string         `json:"opened_gui"`
	AltMode           bool           `json:"alt_mode"`    // Alt: значки содержимого на постройках
	RenderMode        string         `json:"render_mode"` // "game"; "chart"/"chart_zoomed_in" — открыта карта // "controller" — инвентарь, "entity" — окно сущности
	Opened            *Entity        `json:"opened"`
	Cursor            *Stack         `json:"cursor"`
	Inventory         map[string]int `json:"inventory"`
	Quickbar          List[string]   `json:"quickbar"`
	InventorySlots    List[Slot]     `json:"inventory_slots"`
	InventorySize     int            `json:"inventory_size"`
	CraftingQueue     List[Craft]    `json:"crafting_queue"`
	CraftingQueueSize int            `json:"crafting_queue_size"`
	Character         *Character     `json:"character"`
}

type Event struct {
	Seq     int       `json:"seq"`
	Tick    int       `json:"tick"`
	Type    string    `json:"type"`
	Entity  *Entity   `json:"entity,omitempty"`
	Item    string    `json:"item,omitempty"`
	Count   int       `json:"count,omitempty"`
	Recipe  string    `json:"recipe,omitempty"`
	Tech    string    `json:"tech,omitempty"`
	GUIType int       `json:"gui_type,omitempty"`
	Chunk   *ChunkPos `json:"chunk,omitempty"`
}

// Методы ------------------------------------------------------------------

type Pong struct {
	Tick        int    `json:"tick"`
	Version     string `json:"version"`      // мод botbridge
	GameVersion string `json:"game_version"` // base
}

func (b *Bridge) Ping() (Pong, error) {
	var p Pong
	return p, b.Call("ping", nil, &p)
}

func (b *Bridge) Player() (Player, error) {
	var p Player
	return p, b.Call("get_player", nil, &p)
}

type FindArgs struct {
	Type     string  `json:"type,omitempty"`
	Name     string  `json:"name,omitempty"`
	Radius   float64 `json:"radius,omitempty"`
	Limit    int     `json:"limit,omitempty"`
	Position *Pos    `json:"position,omitempty"`
}

func (b *Bridge) FindNearest(a FindArgs) ([]Entity, error) {
	var es List[Entity]
	return es, b.Call("find_nearest", a, &es)
}

// FindMinable — ближайшие добываемые руками сущности. target: группа
// (tree, rock, wreck, ore, fish, any), имя сущности или имя получаемого предмета.
func (b *Bridge) FindMinable(target string, radius float64, limit int) ([]Entity, error) {
	var es List[Entity]
	return es, b.Call("find_minable", map[string]any{"target": target, "radius": radius, "limit": limit}, &es)
}

// EntityAt — сущность name ровно в точке pos или nil.
func (b *Bridge) EntityAt(name string, pos Pos) (*Entity, error) {
	var e *Entity
	return e, b.Call("entity_at", map[string]any{"name": name, "position": pos}, &e)
}

// PollEvents — события с seq > after; seq — номер последнего события в буфере.
func (b *Bridge) PollEvents(after int) (events []Event, seq int, err error) {
	var r struct {
		Seq    int         `json:"seq"`
		Events List[Event] `json:"events"`
	}
	err = b.Call("poll_events", map[string]any{"after": after}, &r)
	return r.Events, r.Seq, err
}

// Сенсоры карты (стадия 2) ---------------------------------------------------

type ChunkPos struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type ScanResource struct {
	Name   string  `json:"n"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Amount int     `json:"a"`
}

type ScanEntity struct {
	Name      string  `json:"n"`
	Type      string  `json:"t"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Direction *int    `json:"d,omitempty"`
	Unit      int     `json:"u,omitempty"`
	Own       bool    `json:"own,omitempty"`
	Status    string  `json:"st,omitempty"`
	Box       *Box    `json:"bb,omitempty"` // настоящая рамка коллизии (утёсы: зависит от ориентации)
}

// ChunkScan — содержимое открытого чанка. Tiles — 1024 символа по строкам:
// '.' суша, 'w' вода, 's' мелководье (проходимо), 'x' край карты.
type ChunkScan struct {
	X         int                `json:"x"`
	Y         int                `json:"y"`
	Charted   bool               `json:"charted"`
	Tiles     string             `json:"tiles"`
	Resources List[ScanResource] `json:"resources"`
	Entities  List[ScanEntity]   `json:"entities"`
}

func (b *Bridge) ListCharted() ([]ChunkPos, error) {
	var cs List[ChunkPos]
	return cs, b.Call("list_charted", nil, &cs)
}

func (b *Bridge) ScanChunks(chunks []ChunkPos) ([]ChunkScan, error) {
	var res List[ChunkScan]
	return res, b.Call("scan_chunks", map[string]any{"chunks": chunks}, &res)
}

type Force struct {
	Researched List[string] `json:"researched"`
	Current    string       `json:"current"`
	Progress   float64      `json:"progress"`
	Queue      List[string] `json:"queue"`
	Recipes    List[string] `json:"recipes"`
	// произведено предметов за игру (статистика производства)
	Produced map[string]float64 `json:"produced,omitempty"`
}

func (b *Bridge) Force() (Force, error) {
	var f Force
	return f, b.Call("get_force", nil, &f)
}

// DumpPrototypes — справочник kind (recipes, items, entities, technologies, fluids) в out.
func (b *Bridge) DumpPrototypes(kind string, out any) error {
	return b.Call("dump_prototypes", map[string]any{"kind": kind}, out)
}

// EntityState — состояние сущности: инвентари по имени (fuel, furnace_source, furnace_result, chest…).
type EntityState struct {
	Entity
	Status           string                    `json:"status"`
	Own              bool                      `json:"own"`
	Inventories      map[string]map[string]int `json:"inventories"`
	Recipe           string                    `json:"recipe"`
	CraftingProgress float64                   `json:"crafting_progress"`
	Burning          string                    `json:"burning"`
	RemainingFuel    float64                   `json:"remaining_fuel"`
	Network          int                       `json:"network,omitempty"`   // электросеть (0 — нет)
	Belt             map[string]int            `json:"belt,omitempty"`      // что лежит на ленте
	UGType           string                    `json:"ug_type,omitempty"`   // подземка: input/output
	UGPaired         bool                      `json:"ug_paired,omitempty"` // у подземки есть пара
	Filters          List[string]              `json:"filters,omitempty"`   // фильтр инсертера (включён)
	Fluids           map[string]float64        `json:"fluids,omitempty"`    // жидкости в ящиках
	I                int                       `json:"i,omitempty"`         // номер в запросе GetEntities (с 1)
}

// Count — сколько предмета во всех инвентарях сущности.
func (s *EntityState) Count(item string) int {
	n := 0
	for _, inv := range s.Inventories {
		n += inv[item]
	}
	return n
}

// GetEntity — состояние сущности name в точке pos или nil.
func (b *Bridge) GetEntity(name string, pos Pos) (*EntityState, error) {
	var e *EntityState
	return e, b.Call("get_entity", map[string]any{"name": name, "position": pos}, &e)
}

// EntityRef — постройка по имени и месту.
type EntityRef struct {
	Name     string `json:"name"`
	Position Pos    `json:"position"`
}

// GetEntities — GetEntity пакетом: ответ по порядку refs, nil — нет.
func (b *Bridge) GetEntities(refs []EntityRef) ([]*EntityState, error) {
	res := make([]*EntityState, len(refs))
	if len(refs) == 0 {
		return res, nil
	}
	var got List[EntityState]
	if err := b.Call("get_entities", map[string]any{"list": refs}, &got); err != nil {
		return nil, err
	}
	for i := range got {
		if k := got[i].I - 1; k >= 0 && k < len(res) {
			res[k] = &got[i]
		}
	}
	return res, nil
}
