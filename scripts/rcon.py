#!/usr/bin/env python3
# Интерактивная RCON-консоль: scripts/rcon.py [команда]. Без аргумента — REPL.
# Как модуль: from rcon import Rcon; r = Rcon(); r.cmd("/sc rcon.print(game.tick)")
import os, socket, struct, sys

HOST, PORT = "127.0.0.1", 27015
PASSWORD = os.environ.get("RCON_PASSWORD", "botpass")


class Rcon:
    def __init__(self, host=HOST, port=PORT, password=PASSWORD):
        self.s = socket.create_connection((host, port))
        self._send(1, 3, password)
        self._recv()

    def _send(self, i, t, body):
        b = body.encode() + b"\0\0"
        self.s.sendall(struct.pack("<iii", len(b) + 8, i, t) + b)

    def _read(self, n):
        d = b""
        while len(d) < n:
            d += self.s.recv(n - len(d))
        return d

    def _recv(self):
        n = struct.unpack("<i", self._read(4))[0]
        return self._read(n)[8:-2].decode(errors="replace")

    def cmd(self, c):
        self._send(2, 2, c)
        return self._recv()


if __name__ == "__main__":
    r = Rcon()
    if len(sys.argv) > 1:
        print(r.cmd(" ".join(sys.argv[1:])), end="")
        sys.exit()
    while True:
        try:
            c = input("rcon> ")
        except (EOFError, KeyboardInterrupt):
            break
        if c.strip():
            print(r.cmd(c), end="")
