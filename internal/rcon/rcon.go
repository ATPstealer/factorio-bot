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
	mu       sync.Mutex
	conn     net.Conn
	r        *bufio.Reader
	nextID   int32
	addr     string
	password string
	timeout  time.Duration
}

// execTimeout — ответа на команду дольше нет — соединение заново и повтор (ответ однажды не
// пришёл вовсе: бот ждал 34 минуты, стадия 20 на 859088572).
const execTimeout = 60 * time.Second

// Dial подключается и авторизуется.
func Dial(addr, password string, timeout time.Duration) (*Client, error) {
	c := &Client{addr: addr, password: password, timeout: timeout}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) connect() error {
	conn, err := net.DialTimeout("tcp", c.addr, c.timeout)
	if err != nil {
		return err
	}
	c.conn, c.r, c.nextID = conn, bufio.NewReader(conn), 1
	conn.SetDeadline(time.Now().Add(execTimeout))
	if err := c.auth(c.password); err != nil {
		conn.Close()
		return err
	}
	return nil
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
	res, err := c.exec(cmd)
	var ne net.Error
	if err != nil && (errors.As(err, &ne) && ne.Timeout() || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
		// соединение заново и ещё раз (команды бота — только чтение и сообщения в консоль)
		c.conn.Close()
		if cerr := c.connect(); cerr != nil {
			return "", fmt.Errorf("rcon: %v; переподключение: %w", err, cerr)
		}
		res, err = c.exec(cmd)
	}
	return res, err
}

func (c *Client) exec(cmd string) (string, error) {
	c.conn.SetDeadline(time.Now().Add(execTimeout))
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
