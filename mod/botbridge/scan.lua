-- Сканирование карты: только открытые (charted) чанки — то, что игрок видит на карте.
local util = require("util")

local CHUNK = 32

-- Классы тайлов по слоям коллизий: 'w' — вода (не пройти), 's' — мелководье (пройти можно),
-- 'x' — край карты. Остальное — суша '.'.
local tile_classes
local function classes()
  if tile_classes then return tile_classes end
  tile_classes = { w = {}, s = {}, x = {} }
  for name, pr in pairs(prototypes.tile) do
    local L = pr.collision_mask.layers
    if L["out_of_map"] then
      table.insert(tile_classes.x, name)
    elseif L["water_tile"] and L["player"] then
      table.insert(tile_classes.w, name)
    elseif L["water_tile"] then
      table.insert(tile_classes.s, name)
    end
  end
  return tile_classes
end

local status_name = util.status_name

-- tiles: строка 32×32 по строкам (y, затем x), символ на тайл.
local function scan_tiles(surface, area)
  local grid = {}
  for i = 1, CHUNK * CHUNK do grid[i] = "." end
  local lx, ly = area.left_top.x, area.left_top.y
  for ch, names in pairs(classes()) do
    if #names > 0 then
      for _, t in pairs(surface.find_tiles_filtered { area = area, name = names }) do
        local x, y = t.position.x - lx, t.position.y - ly
        grid[y * CHUNK + x + 1] = ch
      end
    end
  end
  return table.concat(grid)
end

local function in_chunk(p, cx, cy)
  return math.floor(p.x / CHUNK) == cx and math.floor(p.y / CHUNK) == cy
end

local function scan_chunk(p, cx, cy)
  if not util.chunk_visible(p, cx, cy) then
    return { x = cx, y = cy, charted = false }
  end
  local s = p.surface
  local area = { left_top = { x = cx * CHUNK, y = cy * CHUNK }, right_bottom = { x = (cx + 1) * CHUNK, y = (cy + 1) * CHUNK } }
  local res = { x = cx, y = cy, charted = true, tiles = scan_tiles(s, area), resources = {}, entities = {} }
  for _, e in pairs(s.find_entities_filtered { area = area }) do
    -- сущность, задевающая несколько чанков, попадает в выдачу каждого — берём по позиции
    if e.valid and e.type ~= "character" and in_chunk(e.position, cx, cy) then
      if e.type == "resource" then
        res.resources[#res.resources + 1] = { n = e.name, x = e.position.x, y = e.position.y, a = e.amount }
      else
        local own = e.force == p.force
        res.entities[#res.entities + 1] = {
          n = e.name, t = e.type, x = e.position.x, y = e.position.y,
          d = e.supports_direction and e.direction or nil,
          u = e.unit_number,
          own = own or nil,
          st = own and status_name(e.status) or nil,
          -- рамка зависит от ориентации (утёсы) — отдаём настоящую
          bb = e.type == "cliff" and util.box(e.bounding_box) or nil,
        }
      end
    end
  end
  return res
end

return function(methods)
  -- Открытые чанки: [{x, y}, ...].
  function methods.list_charted()
    local p = util.bot_player()
    local res = {}
    for ch in p.surface.get_chunks() do
      if util.chunk_visible(p, ch.x, ch.y) then res[#res + 1] = { x = ch.x, y = ch.y } end
    end
    return res
  end

  -- Содержимое чанков: args {chunks = [{x, y}, ...]}. Неоткрытый чанк — {charted=false}.
  function methods.scan_chunks(a)
    local p = util.bot_player()
    local res = {}
    for i, c in ipairs(a.chunks or {}) do
      res[i] = scan_chunk(p, c.x, c.y)
    end
    return res
  end

  -- Исследования своей силы.
  function methods.get_force()
    local f = util.bot_player().force
    local researched = {}
    for name, t in pairs(f.technologies) do
      if t.researched then researched[#researched + 1] = name end
    end
    table.sort(researched)
    local queue = {}
    for _, t in pairs(f.research_queue or {}) do queue[#queue + 1] = t.name end
    local recipes = {}
    for name, r in pairs(f.recipes) do
      if r.enabled then recipes[#recipes + 1] = name end
    end
    table.sort(recipes)
    -- сколько произведено (окно производства игрока): для trigger-технологий «скрафтить N»
    local produced = util.try(function()
      return f.get_item_production_statistics(util.bot_player().surface).input_counts
    end)
    return {
      produced = util.nonempty(produced),
      researched = researched,
      current = f.current_research and f.current_research.name or nil,
      progress = f.research_progress,
      queue = queue,
      recipes = recipes,
    }
  end

end
