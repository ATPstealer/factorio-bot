package bridge

import (
	"strings"
	"testing"
)

type fakeExec struct {
	got  string
	resp string
}

func (f *fakeExec) Exec(cmd string) (string, error) {
	f.got = cmd
	return f.resp, nil
}

func TestCommandQuoting(t *testing.T) {
	cases := map[string]string{
		`{"method":"ping"}`:   `/sc remote.call("bot","call",[=[{"method":"ping"}]=])`,
		`{"a":"x]=]y"}`:       `/sc remote.call("bot","call",[==[{"a":"x]=]y"}]==])`,
		`{"a":"\"q\" \\ ]]"}`: `/sc remote.call("bot","call",[=[{"a":"\"q\" \\ ]]"}]=])`,
	}
	for in, want := range cases {
		if got := command(in); got != want {
			t.Errorf("command(%s) = %s, ждали %s", in, got, want)
		}
	}
}

func TestCallOK(t *testing.T) {
	f := &fakeExec{resp: `{"ok":true,"result":{"tick":42,"version":"0.2.0"}}`}
	p, err := New(f).Ping()
	if err != nil {
		t.Fatal(err)
	}
	if p.Tick != 42 || p.Version != "0.2.0" {
		t.Fatalf("pong = %+v", p)
	}
	if !strings.Contains(f.got, `"method":"ping"`) {
		t.Fatalf("команда %s", f.got)
	}
}

func TestCallError(t *testing.T) {
	f := &fakeExec{resp: `{"ok":false,"error":"unknown method: x"}`}
	err := New(f).Call("x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown method") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlayerDecode(t *testing.T) {
	f := &fakeExec{resp: `{"ok":true,"result":{"position":{"x":1.5,"y":-2},"zoom":1.5,
		"resolution":{"width":1856,"height":1016},"selected":{"name":"tree-08","type":"tree","position":{"x":3,"y":4}},
		"quickbar":["","wood"],"crafting_queue":{},"character":{"reach":10,"walking":true}}}`}
	p, err := New(f).Player()
	if err != nil {
		t.Fatal(err)
	}
	if p.Position.X != 1.5 || p.Resolution.Width != 1856 || p.Selected.Name != "tree-08" || !p.Character.Walking {
		t.Fatalf("player = %+v", p)
	}
	if p.Inventory != nil || p.Cursor != nil {
		t.Fatalf("пустые поля должны быть nil: %+v", p)
	}
}
