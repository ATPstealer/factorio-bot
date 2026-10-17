// Package bridge — типизированные вызовы read-only интерфейса мода botbridge по RCON.
package bridge

import (
	"encoding/json"
	"fmt"
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

// Box — прямоугольник в тайлах (selection_box, bounding_box).
type Box struct {
	LeftTop     Pos `json:"left_top"`
	RightBottom Pos `json:"right_bottom"`
}

func (b Box) Center() Pos {
	return Pos{X: (b.LeftTop.X + b.RightBottom.X) / 2, Y: (b.LeftTop.Y + b.RightBottom.Y) / 2}
}

type Entity struct {
	Name         string  `json:"name"`
	Type         string  `json:"type"`
	Position     Pos     `json:"position"`
	UnitNumber   int     `json:"unit_number,omitempty"`
	SelectionBox *Box    `json:"selection_box,omitempty"`
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

type Craft struct {
	Recipe string `json:"recipe"`
	Count  int    `json:"count"`
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
	Selected          *Entity        `json:"selected"`
	OpenedGUIType     *int           `json:"opened_gui_type"`
	Opened            *Entity        `json:"opened"`
	Cursor            *Stack         `json:"cursor"`
	Inventory         map[string]int `json:"inventory"`
	Quickbar          List[string]   `json:"quickbar"`
	CraftingQueue     List[Craft]    `json:"crafting_queue"`
	CraftingQueueSize int            `json:"crafting_queue_size"`
	Character         *Character     `json:"character"`
}

type Event struct {
	Seq     int     `json:"seq"`
	Tick    int     `json:"tick"`
	Type    string  `json:"type"`
	Entity  *Entity `json:"entity,omitempty"`
	Item    string  `json:"item,omitempty"`
	Count   int     `json:"count,omitempty"`
	Recipe  string  `json:"recipe,omitempty"`
	Tech    string  `json:"tech,omitempty"`
	GUIType int     `json:"gui_type,omitempty"`
}

// Методы ------------------------------------------------------------------

type Pong struct {
	Tick    int    `json:"tick"`
	Version string `json:"version"`
}

func (b *Bridge) Ping() (Pong, error) {
	var p Pong
	return p, b.Call("ping", nil, &p)
}

func (b *Bridge) Tick() (int, error) {
	var t int
	return t, b.Call("tick", nil, &t)
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
