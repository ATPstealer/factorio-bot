-- Общие функции сенсоров.
local util = {}

function util.pos(p)
  return { x = p.x, y = p.y }
end

function util.box(b)
  return { left_top = util.pos(b.left_top), right_bottom = util.pos(b.right_bottom) }
end

function util.bot_player()
  local p = game.connected_players[1]
  if not p then error("нет подключённого игрока") end
  return p
end

function util.entity_info(e)
  if not (e and e.valid) then return nil end
  return {
    name = e.name, type = e.type, position = util.pos(e.position), unit_number = e.unit_number,
    selection_box = util.box(e.selection_box),
    direction = e.supports_direction and e.direction or nil,
  }
end

-- contents — {имя = количество} инвентаря или nil, если пуст.
function util.contents(inv)
  if not inv then return nil end
  local res = {}
  for _, it in pairs(inv.get_contents()) do
    res[it.name] = (res[it.name] or 0) + it.count
  end
  return util.nonempty(res)
end

local status_names
function util.status_name(s)
  if s == nil then return nil end
  if not status_names then
    status_names = {}
    for k, v in pairs(defines.entity_status) do status_names[v] = k end
  end
  return status_names[s]
end

local render_names
-- render_name — "game" (обычный вид), "chart" / "chart_zoomed_in" (карта / удалённый вид).
function util.render_name(m)
  if not render_names then
    render_names = {}
    for k, v in pairs(defines.render_mode) do render_names[v] = k end
  end
  return render_names[m]
end

local gui_names
-- gui_name — имя defines.gui_type ("controller" — инвентарь персонажа, "entity" — окно сущности).
function util.gui_name(t)
  if t == nil then return nil end
  if not gui_names then
    gui_names = {}
    for k, v in pairs(defines.gui_type) do gui_names[v] = k end
  end
  return gui_names[t]
end

-- Честность: бот знает только то, что видит игрок — открытые (charted) чанки его силы.
function util.chunk_visible(p, cx, cy)
  return p.force.is_chunk_charted(p.surface, { cx, cy })
end

function util.visible(p, position)
  return util.chunk_visible(p, math.floor(position.x / 32), math.floor(position.y / 32))
end

-- try — значение f(...) или nil, если поле/метод не применимо к этому прототипу.
function util.try(f, ...)
  local ok, v = pcall(f, ...)
  if ok then return v end
  return nil
end

-- nonempty — nil вместо пустой таблицы (helpers.table_to_json превращает {} в [], а
-- словари в Go так не декодировать).
function util.nonempty(t)
  if t == nil or next(t) == nil then return nil end
  return t
end

return util
