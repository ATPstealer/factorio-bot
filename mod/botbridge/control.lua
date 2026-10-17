-- botbridge: read-only интерфейс для внешнего бота.
-- Вызов по RCON: /sc remote.call("bot", "call", '{"method":"ping"}')
-- Ответ — одна строка JSON: {"ok":true,"result":...} или {"ok":false,"error":"..."}.
-- Правило: ни один метод не меняет состояние игры.

local methods = {}

function methods.ping()
  return { tick = game.tick, version = script.active_mods["botbridge"] }
end

local function call(json)
  local req = helpers.json_to_table(json or "{}") or {}
  local fn = methods[req.method]
  local res
  if not fn then
    res = { ok = false, error = "unknown method: " .. tostring(req.method) }
  else
    local ok, r = pcall(fn, req.args or {})
    res = ok and { ok = true, result = r } or { ok = false, error = tostring(r) }
  end
  rcon.print(helpers.table_to_json(res))
end

remote.add_interface("bot", { call = call })
