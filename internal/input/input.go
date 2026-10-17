// Package input — эмуляция мыши и клавиатуры через расширение XTEST в X-дисплее клиента (:1).
// Это единственный способ бота действовать в игре.
package input

import (
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
)

const (
	keyPress      = 2
	keyRelease    = 3
	buttonPress   = 4
	buttonRelease = 5
	motionNotify  = 6
)

// Кнопки мыши X11.
const (
	Left      byte = 1
	Middle    byte = 2
	Right     byte = 3
	WheelUp   byte = 4
	WheelDown byte = 5
)

// Rect — окно в координатах корня дисплея.
type Rect struct{ X, Y, W, H int }

type Input struct {
	mu     sync.Mutex
	conn   *xgb.Conn
	root   xproto.Window
	keymap map[xproto.Keysym]keyLoc
	// нажатые сейчас клавиши (keycode) и кнопки — для ReleaseAll
	keys    map[byte]bool
	buttons map[byte]bool
	// Пауза после каждого события: игра читает ввод раз в тик (~16 мс).
	Delay time.Duration
	// Сколько держать клавишу в Tap: нажатие короче ~30 мс игра может не заметить
	// (проверено на Z и E), длинное — включает автоповтор.
	Hold time.Duration
}

type keyLoc struct {
	code  xproto.Keycode
	shift bool
}

// Open подключается к дисплею (например ":1") и загружает раскладку клавиатуры.
func Open(display string) (*Input, error) {
	xgb.Logger = log.New(io.Discard, "", 0) // иначе пишет в stderr про отсутствие Xauthority
	c, err := xgb.NewConnDisplay(display)
	if err != nil {
		return nil, fmt.Errorf("input: дисплей %s: %w", display, err)
	}
	if err := xtest.Init(c); err != nil {
		c.Close()
		return nil, fmt.Errorf("input: XTEST: %w", err)
	}
	setup := xproto.Setup(c)
	in := &Input{conn: c, root: setup.DefaultScreen(c).Root, keymap: map[xproto.Keysym]keyLoc{},
		keys: map[byte]bool{}, buttons: map[byte]bool{}, Delay: 20 * time.Millisecond, Hold: 50 * time.Millisecond}
	n := int(setup.MaxKeycode) - int(setup.MinKeycode) + 1
	km, err := xproto.GetKeyboardMapping(c, setup.MinKeycode, byte(n)).Reply()
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("input: раскладка: %w", err)
	}
	per := int(km.KeysymsPerKeycode)
	for i := 0; i < n; i++ {
		for col := 0; col < per && col < 2; col++ {
			ks := km.Keysyms[i*per+col]
			if ks == 0 {
				continue
			}
			if _, ok := in.keymap[ks]; !ok {
				in.keymap[ks] = keyLoc{code: xproto.Keycode(int(setup.MinKeycode) + i), shift: col == 1}
			}
		}
	}
	return in, nil
}

// Close отпускает всё нажатое и закрывает соединение.
func (in *Input) Close() {
	in.ReleaseAll()
	in.conn.Close()
}

// ReleaseAll отпускает все клавиши и кнопки, нажатые через этот Input.
func (in *Input) ReleaseAll() {
	in.mu.Lock()
	keys, buttons := in.keys, in.buttons
	in.keys, in.buttons = map[byte]bool{}, map[byte]bool{}
	in.mu.Unlock()
	for k := range keys {
		in.fake(keyRelease, k, 0, 0)
	}
	for b := range buttons {
		in.fake(buttonRelease, b, 0, 0)
	}
}

// Reset отпускает кнопки мыши и клавиши, которые бот держит дольше одного события
// (WASD, модификаторы). Нужен при старте: если прошлый процесс убили, X-сервер
// продолжает считать их нажатыми, и персонаж сам идёт или копает.
func (in *Input) Reset() error {
	for _, b := range []byte{Left, Middle, Right} {
		if err := in.fake(buttonRelease, b, 0, 0); err != nil {
			return err
		}
	}
	for _, k := range []string{"w", "a", "s", "d", "shift", "ctrl", "alt"} {
		if err := in.KeyUp(k); err != nil {
			return err
		}
	}
	return nil
}

func (in *Input) fake(typ, detail byte, x, y int) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	xtest.FakeInput(in.conn, typ, detail, 0, in.root, int16(x), int16(y), 0)
	switch typ {
	case keyPress:
		in.keys[detail] = true
	case keyRelease:
		delete(in.keys, detail)
	case buttonPress:
		in.buttons[detail] = true
	case buttonRelease:
		delete(in.buttons, detail)
	}
	// round trip: дождаться, пока сервер обработает событие
	if _, err := xproto.GetInputFocus(in.conn).Reply(); err != nil {
		return fmt.Errorf("input: %w", err)
	}
	time.Sleep(in.Delay)
	return nil
}

// Клавиатура ---------------------------------------------------------------

var named = map[string]xproto.Keysym{
	"escape": 0xff1b, "esc": 0xff1b, "return": 0xff0d, "enter": 0xff0d, "tab": 0xff09,
	"backspace": 0xff08, "delete": 0xffff, "space": 0x20,
	"shift": 0xffe1, "ctrl": 0xffe3, "control": 0xffe3, "alt": 0xffe9,
	"left": 0xff51, "up": 0xff52, "right": 0xff53, "down": 0xff54,
	"home": 0xff50, "end": 0xff57, "pageup": 0xff55, "pagedown": 0xff56,
}

func keysym(name string) (xproto.Keysym, error) {
	l := strings.ToLower(name)
	if ks, ok := named[l]; ok {
		return ks, nil
	}
	if len(l) >= 2 && l[0] == 'f' {
		var n int
		if _, err := fmt.Sscanf(l[1:], "%d", &n); err == nil && n >= 1 && n <= 12 {
			return xproto.Keysym(0xffbe + n - 1), nil
		}
	}
	if len(name) == 1 && name[0] >= 0x20 && name[0] < 0x7f {
		return xproto.Keysym(name[0]), nil // Latin-1: keysym = код символа
	}
	return 0, fmt.Errorf("input: неизвестная клавиша %q", name)
}

func (in *Input) code(name string) (keyLoc, error) {
	ks, err := keysym(name)
	if err != nil {
		return keyLoc{}, err
	}
	loc, ok := in.keymap[ks]
	if !ok {
		return keyLoc{}, fmt.Errorf("input: клавиши %q нет в раскладке", name)
	}
	return loc, nil
}

// KeyDown/KeyUp — нажать/отпустить клавишу по имени: "w", "e", "escape", "ctrl", "f1".
// Буквы — в нижнем регистре; для заглавных используйте Type.
func (in *Input) KeyDown(name string) error {
	loc, err := in.code(strings.ToLower(name))
	if err != nil {
		return err
	}
	return in.fake(keyPress, byte(loc.code), 0, 0)
}

func (in *Input) KeyUp(name string) error {
	loc, err := in.code(strings.ToLower(name))
	if err != nil {
		return err
	}
	return in.fake(keyRelease, byte(loc.code), 0, 0)
}

// Tap — нажать и отпустить; модификаторы держатся на время нажатия: Tap("f", "ctrl").
func (in *Input) Tap(name string, mods ...string) error {
	for _, m := range mods {
		if err := in.KeyDown(m); err != nil {
			return err
		}
	}
	err := in.KeyDown(name)
	if err == nil {
		time.Sleep(in.Hold)
		err = in.KeyUp(name)
	}
	for i := len(mods) - 1; i >= 0; i-- {
		if e := in.KeyUp(mods[i]); err == nil {
			err = e
		}
	}
	return err
}

// Type печатает ASCII-текст (поиск рецептов, техи), с Shift где нужно.
func (in *Input) Type(text string) error {
	shift, _ := in.code("shift")
	for _, r := range text {
		if r < 0x20 || r >= 0x7f {
			return fmt.Errorf("input: Type: символ %q не ASCII", r)
		}
		loc, err := in.code(string(r))
		if err != nil {
			return err
		}
		if loc.shift {
			in.fake(keyPress, byte(shift.code), 0, 0)
		}
		in.fake(keyPress, byte(loc.code), 0, 0)
		in.fake(keyRelease, byte(loc.code), 0, 0)
		if loc.shift {
			in.fake(keyRelease, byte(shift.code), 0, 0)
		}
	}
	return nil
}

// Мышь ---------------------------------------------------------------------

// Move — курсор в точку (координаты корня дисплея).
func (in *Input) Move(x, y int) error { return in.fake(motionNotify, 0, x, y) }

func (in *Input) ButtonDown(b byte) error { return in.fake(buttonPress, b, 0, 0) }
func (in *Input) ButtonUp(b byte) error   { return in.fake(buttonRelease, b, 0, 0) }

func (in *Input) Click(b byte) error {
	if err := in.ButtonDown(b); err != nil {
		return err
	}
	return in.ButtonUp(b)
}

// Окна ---------------------------------------------------------------------

// FindWindow ищет окно верхнего уровня, чьё имя начинается с prefix (в Xephyr нет WM,
// окна клиента — прямые потомки корня).
func (in *Input) FindWindow(prefix string) (Rect, error) {
	tree, err := xproto.QueryTree(in.conn, in.root).Reply()
	if err != nil {
		return Rect{}, err
	}
	for _, w := range tree.Children {
		p, err := xproto.GetProperty(in.conn, false, w, xproto.AtomWmName, xproto.GetPropertyTypeAny, 0, 256).Reply()
		if err != nil || !strings.HasPrefix(string(p.Value), prefix) {
			continue
		}
		attr, err := xproto.GetWindowAttributes(in.conn, w).Reply()
		if err != nil || attr.MapState != xproto.MapStateViewable {
			continue
		}
		g, err := xproto.GetGeometry(in.conn, xproto.Drawable(w)).Reply()
		if err != nil {
			return Rect{}, err
		}
		t, err := xproto.TranslateCoordinates(in.conn, w, in.root, 0, 0).Reply()
		if err != nil {
			return Rect{}, err
		}
		return Rect{X: int(t.DstX), Y: int(t.DstY), W: int(g.Width), H: int(g.Height)}, nil
	}
	return Rect{}, fmt.Errorf("input: окно %q не найдено", prefix)
}
