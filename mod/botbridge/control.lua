-- botbridge: read-only интерфейс для внешнего бота.
-- Вызов по RCON: /sc remote.call("bot", "call", '{"method":"ping","args":{...}}')
-- Ответ — одна строка JSON: {"ok":true,"result":...} или {"ok":false,"error":"..."}.
-- Правило: ни один метод не меняет состояние игры. Мод пишет только в свой storage (буфер событий).

local MAX_EVENTS = 1000

local methods = {}

local util = require("util")
local pos, bot_player, entity_info, visible = util.pos, util.bot_player, util.entity_info, util.visible

require("prototypes")(methods)
require("scan")(methods)

-- get_quick_bar_slot: в 2.0 — прототип или пара {name, quality}; в 2.1 — {type = "filter",
-- filter = {name, quality, comparator}} (бывают и другие виды ячеек — у них имени нет) — берём имя.
local function slot_name(v)
  if v == nil then return nil end
  if type(v) == "string" then return v end
  if v.filter then return v.filter.name end
  return v.name
end

function methods.ping()
  return { tick = game.tick, version = script.active_mods["botbridge"], game_version = script.active_mods["base"] }
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
    controller_name = util.controller_name(p.controller_type),
    selected = entity_info(p.selected),
    opened_gui_type = p.opened_gui_type,
    opened_gui = util.gui_name(p.opened_gui_type),
    render_mode = util.render_name(p.render_mode),
    alt_mode = util.try(function() return p.game_view_settings.show_entity_info end), -- Alt
    opened = p.opened and p.opened.object_name == "LuaEntity" and entity_info(p.opened) or nil,
    cursor = nil,
    inventory = {},
    quickbar = {},
    crafting_queue = {},
    crafting_queue_size = c and p.crafting_queue_size or 0,
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
  -- ячейки инвентаря (1-based): по ним бот считает, куда кликнуть в окне инвентаря
  r.inventory_slots = {}
  if inv then
    for i = 1, #inv do
      local st = inv[i]
      if st.valid_for_read then
        r.inventory_slots[#r.inventory_slots + 1] = { slot = i, name = st.name, count = st.count }
      end
    end
    r.inventory_size = #inv
  end
  for i = 1, 10 do
    local ok, v = pcall(p.get_quick_bar_slot, 1, i) -- 2.1: (страница, ячейка)
    r.quickbar[i] = ok and slot_name(v) or ""
  end
  -- без персонажа (заставка в начале новой карты) очереди крафта нет — обращение к ней ошибка
  for _, q in pairs(c and p.crafting_queue or {}) do
    r.crafting_queue[#r.crafting_queue + 1] = { recipe = q.recipe, count = q.count, prerequisite = q.prerequisite }
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
    if visible(p, e.position) then
      local dx, dy = e.position.x - center.x, e.position.y - center.y
      items[#items + 1] = { e = e, d = dx * dx + dy * dy }
    end
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

-- Добыча руками ------------------------------------------------------------

-- Типы сущностей, которые встречаются на карте и добываются руками.
local NATURAL_TYPES = { "tree", "simple-entity", "simple-entity-with-owner", "resource", "fish", "container" }

local GROUPS = {
  tree = function(e) return e.type == "tree" end,
  rock = function(e) return e.type == "simple-entity" and e.name:find("rock", 1, true) ~= nil end,
  wreck = function(e) return e.name:find("crash-site-", 1, true) == 1 end,
  ore = function(e) return e.type == "resource" end,
  fish = function(e) return e.type == "fish" end,
  any = function() return true end,
}

local function products(e)
  local res = {}
  for _, pr in pairs(e.prototype.mineable_properties.products or {}) do
    res[#res + 1] = { name = pr.name, amount = pr.amount or pr.amount_max or 0 }
  end
  return res
end

-- Добывается ли руками: minable, не нужна жидкость (уран), не построено игроком, не персонаж.
local function hand_minable(e)
  local m = e.prototype.mineable_properties
  return m and m.minable and not m.required_fluid and e.type ~= "character" and e.last_user == nil
end

-- Предикат цели: группа, имя сущности или имя получаемого предмета.
local function target_filter(target)
  if GROUPS[target] then return GROUPS[target] end
  if prototypes.entity[target] then return function(e) return e.name == target end end
  if prototypes.item[target] then
    return function(e)
      for _, pr in pairs(products(e)) do
        if pr.name == target then return true end
      end
      return false
    end
  end
  error("неизвестная цель: " .. tostring(target) .. " (группа " ..
    "tree/rock/wreck/ore/fish/any, имя сущности или предмета)")
end

-- Ближайшие добываемые руками сущности: args {target, radius=64, limit=10, position?=игрок}.
function methods.find_minable(a)
  local p = bot_player()
  local center = a.position or p.position
  local match = target_filter(a.target or "any")
  local items = {}
  for _, e in pairs(p.surface.find_entities_filtered { type = NATURAL_TYPES, position = center, radius = a.radius or 64 }) do
    if hand_minable(e) and match(e) and visible(p, e.position) then
      local dx, dy = e.position.x - center.x, e.position.y - center.y
      items[#items + 1] = { e = e, d = dx * dx + dy * dy }
    end
  end
  table.sort(items, function(x, y) return x.d < y.d end)
  local res = {}
  for i = 1, math.min(a.limit or 10, #items) do
    local e = items[i].e
    local info = entity_info(e)
    info.distance = math.sqrt(items[i].d)
    info.products = products(e)
    info.mining_time = e.prototype.mineable_properties.mining_time
    if e.type == "resource" then info.amount = e.amount end
    res[i] = info
  end
  return res
end

-- Состояние сущности в точке: инвентари, топливо, рецепт, статус. args {name, position}.
-- entity_state — состояние своей/видимой постройки name в position (nil — нет).
local function entity_state(p, name, position)
  if not visible(p, position) then return nil end
  local e = p.surface.find_entities_filtered { name = name, position = position, radius = 0.01 }[1]
  if not e then return nil end
  local r = entity_info(e)
  r.status = e.status and util.status_name(e.status) or nil
  r.own = e.force == p.force or nil
  r.inventories = {}
  for i = 1, e.get_max_inventory_index() do
    local inv = e.get_inventory(i)
    if inv then
      local c = util.contents(inv)
      if c then r.inventories[inv.name or tostring(i)] = c end
    end
  end
  r.inventories = util.nonempty(r.inventories)
  -- ячейки сундука (1-based): по ним бот кликает в окне сундука (взять только нужное)
  if e.type == "container" then
    local inv = e.get_inventory(defines.inventory.chest)
    if inv then
      local s = {}
      for i = 1, #inv do
        local st = inv[i]
        if st.valid_for_read then s[#s + 1] = { slot = i, name = st.name, count = st.count } end
      end
      if #s > 0 then r.slots = s end
      r.slots_size = #inv
    end
  end
  if e.type == "assembling-machine" or e.type == "furnace" then
    local rec = e.get_recipe()
    r.recipe = rec and rec.name or nil
    r.crafting_progress = e.crafting_progress
  end
  r.network = util.try(function() return e.electric_network_id end)
  if e.type == "inserter" and e.use_filters then -- фильтр «что брать»
    local f = {}
    for i = 1, e.filter_slot_count do
      local x = e.get_filter(i)
      if x then f[#f + 1] = x.name end
    end
    if #f > 0 then r.filters = f end
  end
  if e.type == "underground-belt" then -- input/output, есть ли пара
    r.ug_type = e.belt_to_ground_type
    r.ug_paired = e.underground_belt_neighbour ~= nil or nil -- 2.1: neighbours убрали
  end
  if e.type == "transport-belt" then -- что лежит на ленте (обе полосы)
    local items = {}
    for i = 1, 2 do
      for _, it in pairs(e.get_transport_line(i).get_contents()) do
        items[it.name] = (items[it.name] or 0) + it.count
      end
    end
    r.belt = util.nonempty(items)
  end
  -- жидкости в ящиках: имя → количество (2.1: LuaEntity::fluidbox убрали — get_fluid_contents)
  local okf, fc = pcall(function() return e.get_fluid_contents() end)
  if okf and fc then
    local f = {}
    for name, amount in pairs(fc) do f[name] = amount end
    r.fluids = util.nonempty(f)
  end
  if e.burner then
    local cb = e.burner.currently_burning
    -- 2.0: {name = прототип предмета, quality}
    r.burning = cb and (type(cb.name) == "string" and cb.name or cb.name.name) or nil
    r.remaining_fuel = e.burner.remaining_burning_fuel
  end
  return r
end

function methods.get_entity(a)
  return entity_state(bot_player(), a.name, a.position)
end

-- Пакетом (снимок состояния бота: сотни построек — один запрос вместо сотен):
-- list = [{name, position}], ответ — [{i = номер в list, ...состояние}] найденных.
function methods.get_entities(a)
  local p = bot_player()
  local res = {}
  for i, q in ipairs(a.list or {}) do
    local r = entity_state(p, q.name, q.position)
    if r then
      r.i = i
      res[#res + 1] = r
    end
  end
  return res
end

-- Есть ли сущность с таким именем ровно в этой точке.
function methods.entity_at(a)
  local p = bot_player()
  if not visible(p, a.position) then return nil end
  local e = p.surface.find_entities_filtered { name = a.name, position = a.position, radius = 0.01 }[1]
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
-- новый сгенерированный чанк (персонаж ходит — игра генерирует вокруг): модели мира — как
-- открытый (видна вся сгенерированная карта)
script.on_event(d.on_chunk_generated, function(e)
  local p = game.connected_players[1]
  if p and e.surface.index == p.surface.index then
    push { type = "chunk_charted", chunk = { x = e.position.x, y = e.position.y } }
  end
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
