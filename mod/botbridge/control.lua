-- botbridge: read-only интерфейс для внешнего бота.
-- Вызов по RCON: /sc remote.call("bot", "call", '{"method":"ping","args":{...}}')
-- Ответ — одна строка JSON: {"ok":true,"result":...} или {"ok":false,"error":"..."}.
-- Правило: ни один метод не меняет состояние игры. Мод пишет только в свой storage (буфер событий).

local MAX_EVENTS = 1000

local methods = {}

local function pos(p)
  return { x = p.x, y = p.y }
end

local function bot_player()
  local p = game.connected_players[1]
  if not p then error("нет подключённого игрока") end
  return p
end

local function entity_info(e)
  if not (e and e.valid) then return nil end
  local sb = e.selection_box
  return {
    name = e.name, type = e.type, position = pos(e.position), unit_number = e.unit_number,
    selection_box = { left_top = pos(sb.left_top), right_bottom = pos(sb.right_bottom) },
  }
end

-- В 2.0 get_quick_bar_slot возвращает прототип или пару {name, quality} — берём имя.
local function slot_name(v)
  if v == nil then return nil end
  if type(v) == "string" then return v end
  return v.name
end

function methods.ping()
  return { tick = game.tick, version = script.active_mods["botbridge"] }
end

function methods.tick()
  return game.tick
end

-- Состояние игрока: всё, что нужно для ввода и проверки его результата.
function methods.get_player()
  local p = bot_player()
  local c = p.character
  local r = {
    name = p.name,
    index = p.index,
    position = pos(p.position),
    zoom = p.zoom,
    resolution = { width = p.display_resolution.width, height = p.display_resolution.height },
    scale = p.display_scale,
    controller = p.controller_type,
    selected = entity_info(p.selected),
    opened_gui_type = p.opened_gui_type,
    opened = p.opened and p.opened.object_name == "LuaEntity" and entity_info(p.opened) or nil,
    cursor = nil,
    inventory = {},
    quickbar = {},
    crafting_queue = {},
    crafting_queue_size = p.crafting_queue_size,
  }
  if p.cursor_stack and p.cursor_stack.valid_for_read then
    r.cursor = { name = p.cursor_stack.name, count = p.cursor_stack.count }
  end
  local inv = p.get_main_inventory()
  if inv then
    for _, it in pairs(inv.get_contents()) do
      r.inventory[it.name] = (r.inventory[it.name] or 0) + it.count
    end
  end
  if next(r.inventory) == nil then r.inventory = nil end -- пустая таблица ушла бы в JSON как []
  for i = 1, 10 do
    local ok, v = pcall(p.get_quick_bar_slot, i)
    r.quickbar[i] = ok and slot_name(v) or ""
  end
  for _, q in pairs(p.crafting_queue or {}) do
    r.crafting_queue[#r.crafting_queue + 1] = { recipe = q.recipe, count = q.count }
  end
  if c then
    r.character = {
      reach = c.reach_distance,
      resource_reach = c.resource_reach_distance,
      walking = c.walking_state.walking,
      mining = c.mining_state.mining,
      speed = c.character_running_speed,
    }
  end
  return r
end

-- Ближайшие к точке сущности: args {type?, name?, radius=32, limit=1, position?=игрок}.
function methods.find_nearest(a)
  local p = bot_player()
  local center = a.position or p.position
  local list = p.surface.find_entities_filtered {
    type = a.type, name = a.name, position = center, radius = a.radius or 32,
  }
  local items = {}
  for _, e in pairs(list) do
    local dx, dy = e.position.x - center.x, e.position.y - center.y
    items[#items + 1] = { e = e, d = dx * dx + dy * dy }
  end
  table.sort(items, function(x, y) return x.d < y.d end)
  local res = {}
  for i = 1, math.min(a.limit or 1, #items) do
    local info = entity_info(items[i].e)
    info.distance = math.sqrt(items[i].d)
    res[i] = info
  end
  return res
end

-- Есть ли сущность с таким именем ровно в этой точке.
function methods.entity_at(a)
  local e = game.surfaces[1].find_entities_filtered { name = a.name, position = a.position, radius = 0.01 }[1]
  return entity_info(e)
end

-- События с номером > after (без удаления из буфера): args {after=0}.
function methods.poll_events(a)
  local after = a.after or 0
  local res = {}
  for _, ev in ipairs(storage.events) do
    if ev.seq > after then res[#res + 1] = ev end
  end
  return { seq = storage.seq, events = res }
end

-- Буфер событий ----------------------------------------------------------

local function init()
  storage.events = storage.events or {}
  storage.seq = storage.seq or 0
end

local function push(ev)
  storage.seq = storage.seq + 1
  ev.seq = storage.seq
  ev.tick = game.tick
  local buf = storage.events
  buf[#buf + 1] = ev
  if #buf > MAX_EVENTS then table.remove(buf, 1) end
end

script.on_init(init)
script.on_configuration_changed(init)

local d = defines.events
script.on_event(d.on_player_mined_entity, function(e)
  push { type = "mined_entity", entity = entity_info(e.entity) }
end)
script.on_event(d.on_built_entity, function(e)
  push { type = "built_entity", entity = entity_info(e.entity) }
end)
script.on_event(d.on_player_crafted_item, function(e)
  push { type = "crafted_item", item = e.item_stack.name, count = e.item_stack.count, recipe = e.recipe.name }
end)
script.on_event(d.on_research_finished, function(e)
  push { type = "research_finished", tech = e.research.name }
end)
script.on_event(d.on_entity_died, function(e)
  push { type = "entity_died", entity = entity_info(e.entity) }
end)
script.on_event(d.on_resource_depleted, function(e)
  push { type = "resource_depleted", entity = entity_info(e.entity) }
end)
script.on_event(d.on_gui_opened, function(e)
  push { type = "gui_opened", gui_type = e.gui_type, entity = entity_info(e.entity) }
end)
script.on_event(d.on_gui_closed, function(e)
  push { type = "gui_closed", gui_type = e.gui_type, entity = entity_info(e.entity) }
end)

-- Точка входа ------------------------------------------------------------

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
