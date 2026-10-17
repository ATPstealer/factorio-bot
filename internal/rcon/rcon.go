// Package rcon — клиент Source RCON для headless-сервера Factorio.
package rcon

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	typeAuth         = 3
	typeAuthResponse = 2
	typeExec         = 2
	typeResponse     = 0

	maxPacket = 1 << 24
)

var ErrAuth = errors.New("rcon: неверный пароль")

type Client struct {
	mu     sync.Mutex
	conn   net.Conn
	r      *bufio.Reader
	nextID int32
}

// Dial подключается и авторизуется.
func Dial(addr, password string, timeout time.Duration) (*Client, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, r: bufio.NewReader(conn), nextID: 1}
	if err := c.auth(password); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) auth(password string) error {
	id := c.id()
	if err := c.write(id, typeAuth, password); err != nil {
		return err
	}
	// Сервер может прислать пустой RESPONSE_VALUE перед AUTH_RESPONSE.
	for {
		gotID, typ, _, err := c.read()
		if err != nil {
			return err
		}
		if typ != typeAuthResponse {
			continue
		}
		if gotID == -1 {
			return ErrAuth
		}
		if gotID != id {
			return fmt.Errorf("rcon: auth: id %d, ждали %d", gotID, id)
		}
		return nil
	}
}

// Exec отправляет консольную команду и возвращает текст ответа.
func (c *Client) Exec(cmd string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.id()
	if err := c.write(id, typeExec, cmd); err != nil {
		return "", err
	}
	for {
		gotID, typ, body, err := c.read()
		if err != nil {
			return "", err
		}
		if gotID == id && typ == typeResponse {
			return body, nil
		}
	}
}

func (c *Client) id() int32 {
	id := c.nextID
	c.nextID++
	return id
}

func (c *Client) write(id, typ int32, body string) error {
	buf := make([]byte, 12+len(body)+2)
	binary.LittleEndian.PutUint32(buf[0:], uint32(len(body)+10))
	binary.LittleEndian.PutUint32(buf[4:], uint32(id))
	binary.LittleEndian.PutUint32(buf[8:], uint32(typ))
	copy(buf[12:], body)
	_, err := c.conn.Write(buf)
	return err
}

func (c *Client) read() (id, typ int32, body string, err error) {
	var size int32
	if err = binary.Read(c.r, binary.LittleEndian, &size); err != nil {
		return
	}
	if size < 10 || size > maxPacket {
		err = fmt.Errorf("rcon: размер пакета %d", size)
		return
	}
	buf := make([]byte, size)
	if _, err = io.ReadFull(c.r, buf); err != nil {
		return
	}
	id = int32(binary.LittleEndian.Uint32(buf[0:]))
	typ = int32(binary.LittleEndian.Uint32(buf[4:]))
	body = string(buf[8 : size-2])
	return
}
