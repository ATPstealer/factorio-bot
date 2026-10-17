package rcon

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeServer — минимальный RCON-сервер: пароль "pw", на команду отвечает "echo:<команда>".
func fakeServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(conn)
		}
	}()
	return ln.Addr().String()
}

func serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	send := func(id, typ int32, body string) {
		buf := make([]byte, 14+len(body))
		binary.LittleEndian.PutUint32(buf[0:], uint32(len(body)+10))
		binary.LittleEndian.PutUint32(buf[4:], uint32(id))
		binary.LittleEndian.PutUint32(buf[8:], uint32(typ))
		copy(buf[12:], body)
		conn.Write(buf)
	}
	for {
		var size int32
		if binary.Read(r, binary.LittleEndian, &size) != nil {
			return
		}
		buf := make([]byte, size)
		if _, err := io.ReadFull(r, buf); err != nil {
			return
		}
		id := int32(binary.LittleEndian.Uint32(buf[0:]))
		typ := int32(binary.LittleEndian.Uint32(buf[4:]))
		body := string(buf[8 : size-2])
		switch typ {
		case typeAuth:
			send(id, typeResponse, "")
			if body == "pw" {
				send(id, typeAuthResponse, "")
			} else {
				send(-1, typeAuthResponse, "")
			}
		case typeExec:
			send(id, typeResponse, "echo:"+body)
		}
	}
}

func TestExec(t *testing.T) {
	c, err := Dial(fakeServer(t), "pw", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, cmd := range []string{"/sc rcon.print(1)", "", strings.Repeat("x", 100000)} {
		got, err := c.Exec(cmd)
		if err != nil {
			t.Fatal(err)
		}
		if got != "echo:"+cmd {
			t.Fatalf("Exec(%.20q) = %.30q", cmd, got)
		}
	}
}

func TestBadPassword(t *testing.T) {
	_, err := Dial(fakeServer(t), "wrong", time.Second)
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("err = %v, ждали ErrAuth", err)
	}
}
