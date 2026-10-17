// Package knowledge — справочник игры (прототипы рецептов, предметов, сущностей, технологий).
// Берётся из мода один раз на версию игры и кэшируется в data/prototypes-<версия>.json.
package knowledge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"factorio-bot/internal/bridge"
)

type List[T any] = bridge.List[T]

type Amount struct {
	Type        string  `json:"type,omitempty"`
	Name        string  `json:"name"`
	Amount      float64 `json:"amount,omitempty"`
	AmountMin   float64 `json:"amount_min,omitempty"`
	AmountMax   float64 `json:"amount_max,omitempty"`
	Probability float64 `json:"probability,omitempty"` // 0 — значит 1
}

// Mean — среднее количество с учётом диапазона и вероятности.
func (a Amount) Mean() float64 {
	n := a.Amount
	if n == 0 && a.AmountMax > 0 {
		n = (a.AmountMin + a.AmountMax) / 2
	}
	if a.Probability > 0 {
		n *= a.Probability
	}
	return n
}

type Recipe struct {
	Category    string       `json:"category"`
	Energy      float64      `json:"energy"` // секунды крафта при скорости 1
	Ingredients List[Amount] `json:"ingredients"`
	Products    List[Amount] `json:"products"`
	MainProduct string       `json:"main_product,omitempty"`
	Enabled     bool         `json:"enabled"` // доступен с начала игры
	Hidden      bool         `json:"hidden"`
}

type Item struct {
	Type         string  `json:"type"`
	Subgroup     string  `json:"subgroup,omitempty"`
	StackSize    int     `json:"stack_size"`
	PlaceResult  string  `json:"place_result,omitempty"`
	FuelValue    float64 `json:"fuel_value,omitempty"` // Дж
	FuelCategory string  `json:"fuel_category,omitempty"`
	BurntResult  string  `json:"burnt_result,omitempty"`
	Hidden       bool    `json:"hidden"`
}

type Mineable struct {
	MiningTime    float64      `json:"mining_time"`
	Products      List[Amount] `json:"products"`
	RequiredFluid string       `json:"required_fluid,omitempty"`
}

type Burner struct {
	FuelCategories    List[string] `json:"fuel_categories"`
	Effectivity       float64      `json:"effectivity"`
	FuelInventorySize int          `json:"fuel_inventory_size"`
}

type Entity struct {
	Type                   string       `json:"type"`
	TileWidth              int          `json:"tile_width"`
	TileHeight             int          `json:"tile_height"`
	CollisionBox           bridge.Box   `json:"collision_box"`
	SelectionBox           bridge.Box   `json:"selection_box"`
	Flags                  List[string] `json:"flags,omitempty"`
	ItemsToPlaceThis       List[string] `json:"items_to_place_this,omitempty"`
	Mineable               *Mineable    `json:"mineable,omitempty"`
	CraftingSpeed          float64      `json:"crafting_speed,omitempty"`
	CraftingCategories     List[string] `json:"crafting_categories,omitempty"`
	MiningSpeed            float64      `json:"mining_speed,omitempty"`
	MiningDrillRadius      float64      `json:"mining_drill_radius,omitempty"`
	ResourceCategories     List[string] `json:"resource_categories,omitempty"`
	ResourceCategory       string       `json:"resource_category,omitempty"`
	InfiniteResource       bool         `json:"infinite_resource,omitempty"`
	EnergyUsage            float64      `json:"energy_usage,omitempty"` // Дж/тик
	MaxEnergyUsage         float64      `json:"max_energy_usage,omitempty"`
	MaxEnergyProduction    float64      `json:"max_energy_production,omitempty"`
	Burner                 *Burner      `json:"burner,omitempty"`
	Electric               bool         `json:"electric,omitempty"`
	SupplyAreaDistance     float64      `json:"supply_area_distance,omitempty"`
	MaxWireDistance        float64      `json:"max_wire_distance,omitempty"`
	BeltSpeed              float64      `json:"belt_speed,omitempty"`
	MaxUndergroundDistance int          `json:"max_underground_distance,omitempty"`
	ResearchingSpeed       float64      `json:"researching_speed,omitempty"`
	LabInputs              List[string] `json:"lab_inputs,omitempty"`
	ModuleSlots            int          `json:"module_slots,omitempty"`
	PumpingSpeed           float64      `json:"pumping_speed,omitempty"`
	FixedRecipe            string       `json:"fixed_recipe,omitempty"`
}

// Trigger — технология 2.0, открываемая действием (скрафтить/добыть/построить), а не наукой.
type Trigger struct {
	Type   string `json:"type"`
	Item   string `json:"item,omitempty"`
	Entity string `json:"entity,omitempty"`
	Count  int    `json:"count,omitempty"`
}

type Technology struct {
	Prerequisites    List[string] `json:"prerequisites,omitempty"`
	UnitCount        float64      `json:"unit_count"`
	UnitCountFormula string       `json:"unit_count_formula,omitempty"`
	UnitTime         float64      `json:"unit_time"` // секунды на единицу
	UnitIngredients  List[Amount] `json:"unit_ingredients"`
	Trigger          *Trigger     `json:"trigger,omitempty"`
	Unlocks          List[string] `json:"unlocks,omitempty"` // рецепты
	Hidden           bool         `json:"hidden"`
	Enabled          bool         `json:"enabled"`
}

type Fluid struct {
	FuelValue float64 `json:"fuel_value,omitempty"`
	Hidden    bool    `json:"hidden"`
}

type Prototypes struct {
	GameVersion  string                `json:"game_version"`
	Recipes      map[string]Recipe     `json:"recipes"`
	Items        map[string]Item       `json:"items"`
	Entities     map[string]Entity     `json:"entities"`
	Technologies map[string]Technology `json:"technologies"`
	Fluids       map[string]Fluid      `json:"fluids"`
}

// Fetch читает справочник из игры.
func Fetch(b *bridge.Bridge, gameVersion string) (*Prototypes, error) {
	p := &Prototypes{GameVersion: gameVersion}
	for kind, out := range map[string]any{
		"recipes": &p.Recipes, "items": &p.Items, "entities": &p.Entities,
		"technologies": &p.Technologies, "fluids": &p.Fluids,
	} {
		if err := b.DumpPrototypes(kind, out); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func CachePath(dir, gameVersion string) string {
	return filepath.Join(dir, "prototypes-"+gameVersion+".json")
}

func Load(path string) (*Prototypes, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Prototypes
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &p, nil
}

func (p *Prototypes) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadOrFetch — кэш dir/prototypes-<версия игры>.json или, если его нет, дамп из игры с сохранением.
func LoadOrFetch(b *bridge.Bridge, dir string) (*Prototypes, error) {
	pong, err := b.Ping()
	if err != nil {
		return nil, err
	}
	path := CachePath(dir, pong.GameVersion)
	if p, err := Load(path); err == nil {
		return p, nil
	}
	p, err := Fetch(b, pong.GameVersion)
	if err != nil {
		return nil, err
	}
	return p, p.Save(path)
}

// RecipesFor — рецепты (не скрытые), у которых item среди продуктов, по имени.
func (p *Prototypes) RecipesFor(item string) []string {
	var res []string
	for name, r := range p.Recipes {
		if r.Hidden {
			continue
		}
		for _, pr := range r.Products {
			if pr.Name == item {
				res = append(res, name)
				break
			}
		}
	}
	sort.Strings(res)
	return res
}

// TechFor — технология, открывающая рецепт ("" если рецепт доступен изначально или не найден).
func (p *Prototypes) TechFor(recipe string) string {
	names := make([]string, 0, len(p.Technologies))
	for n := range p.Technologies {
		names = append(names, n)
	}
	sort.Strings(names) // детерминизм, если рецепт открывают несколько тех
	for _, n := range names {
		for _, r := range p.Technologies[n].Unlocks {
			if r == recipe {
				return n
			}
		}
	}
	return ""
}
