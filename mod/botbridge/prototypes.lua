-- Справочник игры (прототипы): то, что игрок знает из подсказок и Factoriopedia.
-- dump_prototypes{kind}: kind = recipes | items | entities | technologies | fluids.
local util = require("util")
local try = util.try

local function names(t)
  local res = {}
  for k, v in pairs(t or {}) do
    if v == true then res[#res + 1] = k else res[#res + 1] = v end
  end
  table.sort(res)
  return res
end

local function amounts(list)
  local res = {}
  for _, x in pairs(list or {}) do
    res[#res + 1] = {
      type = x.type, name = x.name,
      amount = x.amount, amount_min = x.amount_min, amount_max = x.amount_max,
      probability = x.probability ~= 1 and x.probability or nil,
    }
  end
  return res
end

local dump = {}

function dump.recipes()
  local res = {}
  for name, r in pairs(prototypes.recipe) do
    res[name] = {
      category = r.category,
      energy = r.energy,
      ingredients = amounts(r.ingredients),
      products = amounts(r.products),
      main_product = r.main_product and r.main_product.name or nil,
      enabled = r.enabled,
      hidden = r.hidden,
      -- порядок в окне крафта: вкладка (group) → строка подгруппы (subgroup) → order
      group = r.group.name, group_order = r.group.order,
      subgroup = r.subgroup.name, subgroup_order = r.subgroup.order,
      order = r.order,
    }
  end
  return res
end

function dump.items()
  local res = {}
  for name, it in pairs(prototypes.item) do
    res[name] = {
      type = it.type,
      subgroup = it.subgroup and it.subgroup.name or nil,
      stack_size = it.stack_size,
      place_result = it.place_result and it.place_result.name or nil,
      fuel_value = it.fuel_value > 0 and it.fuel_value or nil,
      fuel_category = it.fuel_category,
      burnt_result = it.burnt_result and it.burnt_result.name or nil,
      hidden = it.hidden,
    }
  end
  return res
end

function dump.fluids()
  local res = {}
  for name, f in pairs(prototypes.fluid) do
    res[name] = { fuel_value = f.fuel_value > 0 and f.fuel_value or nil, hidden = f.hidden }
  end
  return res
end

-- Природные типы, которые не ставятся предметом, но нужны модели мира.
local NATURAL = { resource = true, tree = true, ["simple-entity"] = true, ["simple-entity-with-owner"] = true,
  fish = true, cliff = true, character = true }

function dump.entities()
  local res = {}
  for name, e in pairs(prototypes.entity) do
    local placeable = e.items_to_place_this and #e.items_to_place_this > 0
    if placeable or NATURAL[e.type] or name:find("crash-site-", 1, true) == 1 then
      -- поле, применимое не ко всем типам: чужое — nil вместо ошибки
      local function g(k) return try(function() return e[k] end) end
      local function call(k) return try(function() return e[k]() end) end
      local m = e.mineable_properties
      local burner = g("burner_prototype")
      res[name] = {
        type = e.type,
        tile_width = e.tile_width,
        tile_height = e.tile_height,
        collision_box = util.box(e.collision_box),
        selection_box = util.box(e.selection_box),
        flags = util.nonempty(names(e.flags)),
        items_to_place_this = util.nonempty(names(try(function()
          local r = {}
          for _, it in pairs(e.items_to_place_this) do r[#r + 1] = it.name end
          return r
        end))),
        mineable = m and m.minable and {
          mining_time = m.mining_time,
          products = amounts(m.products),
          required_fluid = m.required_fluid,
          fluid_amount = m.fluid_amount, -- жидкости на 10 операций добычи
        } or nil,
        crafting_speed = call("get_crafting_speed"),
        crafting_categories = util.nonempty(names(g("crafting_categories"))),
        mining_speed = g("mining_speed"),
        mining_drill_radius = g("mining_drill_radius"),
        resource_categories = util.nonempty(names(g("resource_categories"))),
        resource_category = g("resource_category"),
        infinite_resource = e.type == "resource" and g("infinite_resource") or nil,
        energy_usage = g("energy_usage"),
        max_energy_usage = call("get_max_energy_usage"),
        max_energy_production = call("get_max_energy_production"),
        burner = burner and {
          fuel_categories = names(burner.fuel_categories),
          effectivity = burner.effectivity,
          fuel_inventory_size = burner.fuel_inventory_size,
        } or nil,
        electric = g("electric_energy_source_prototype") ~= nil or nil,
        drain = try(function() return e.electric_energy_source_prototype.drain end), -- Дж/тик в простое
        supply_area_distance = call("get_supply_area_distance"),
        max_wire_distance = call("get_max_wire_distance"),
        belt_speed = g("belt_speed"),
        max_underground_distance = g("max_underground_distance"),
        researching_speed = call("get_researching_speed"),
        lab_inputs = util.nonempty(g("lab_inputs")),
        module_slots = g("module_inventory_size"),
        pumping_speed = call("get_pumping_speed"),
        fixed_recipe = g("fixed_recipe"),
        vector_to_place_result = g("vector_to_place_result"),
      }
    end
  end
  return res
end

function dump.technologies()
  local res = {}
  for name, t in pairs(prototypes.technology) do
    local unlocks = {}
    for _, eff in pairs(t.effects or {}) do
      if eff.type == "unlock-recipe" then unlocks[#unlocks + 1] = eff.recipe end
    end
    local trig = t.research_trigger
    res[name] = {
      prerequisites = util.nonempty(names(try(function()
        local r = {}
        for n in pairs(t.prerequisites) do r[#r + 1] = n end
        return r
      end))),
      unit_count = t.research_unit_count,
      unit_count_formula = t.research_unit_count_formula,
      unit_time = t.research_unit_energy and t.research_unit_energy / 60 or nil,
      unit_ingredients = amounts(t.research_unit_ingredients),
      -- 2.0: часть технологий открывается действием (скрафтить/добыть/построить), а не наукой
      trigger = trig and { type = trig.type, item = trig.item and (trig.item.name or trig.item), entity = trig.entity and (trig.entity.name or trig.entity), count = trig.count } or nil,
      unlocks = util.nonempty(unlocks),
      hidden = t.hidden,
      enabled = t.enabled,
    }
  end
  return res
end

return function(methods)
  function methods.dump_prototypes(a)
    local f = dump[a.kind or ""]
    if not f then error("kind: recipes | items | entities | technologies | fluids") end
    return f()
  end
end
