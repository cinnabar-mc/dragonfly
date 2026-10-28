package blockinternal

import (
	"reflect"
	"testing"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/block/customblock"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// TestComponents_CollisionCanBeExplicitlyDisabled separates an omitted box
// (inherit the client default) from an intentionally non-collidable block.
func TestComponents_CollisionCanBeExplicitlyDisabled(t *testing.T) {
	for _, tc := range []struct {
		name             string
		properties       customblock.Properties
		present, enabled bool
	}{
		{"default", customblock.Properties{}, false, false},
		{"disabled", customblock.Properties{DisableCollision: true}, true, false},
		{"box", customblock.Properties{CollisionBox: cube.Box(0, 0, 0, 1, 1, 1)}, true, true},
		{"disabled overrides box", customblock.Properties{DisableCollision: true, CollisionBox: cube.Box(0, 0, 0, 1, 1, 1)}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			components := componentsFromProperties("test:collision", tc.properties)
			component, present := components["minecraft:collision_box"].(map[string]any)
			if present != tc.present {
				t.Fatalf("collision present=%v, want %v", present, tc.present)
			}
			if present && component["enabled"] != tc.enabled {
				t.Fatalf("collision enabled=%v, want %v", component["enabled"], tc.enabled)
			}
		})
	}
}

// taggedBlock is a custom block carrying vanilla block tags.
type taggedBlock struct {
	tags []string
}

func (taggedBlock) EncodeBlock() (string, map[string]any) { return "test:tagged", nil }
func (taggedBlock) Model() world.BlockModel               { return nil }
func (taggedBlock) Hash() (uint64, uint64)                { return 0, 0 }
func (b taggedBlock) Tags() []string                      { return b.tags }

func (taggedBlock) Properties() customblock.Properties {
	return customblock.Properties{Cube: true}
}

// untaggedBlock is the same block without tags, so the component map must not gain the
// field at all rather than gaining an empty one.
type untaggedBlock struct{}

func (untaggedBlock) EncodeBlock() (string, map[string]any) { return "test:untagged", nil }
func (untaggedBlock) Model() world.BlockModel               { return nil }
func (untaggedBlock) Hash() (uint64, uint64)                { return 0, 0 }

func (untaggedBlock) Properties() customblock.Properties {
	return customblock.Properties{Cube: true}
}

// breakableBlock is a custom block with the hardness of stone, harvestable only with a
// tool, so its bare-handed time is the hardness times a hundred ticks rather than thirty.
type breakableBlock struct{}

func (breakableBlock) EncodeBlock() (string, map[string]any) { return "test:breakable", nil }
func (breakableBlock) Model() world.BlockModel               { return nil }
func (breakableBlock) Hash() (uint64, uint64)                { return 0, 0 }

func (breakableBlock) Properties() customblock.Properties {
	return customblock.Properties{Cube: true}
}

func (breakableBlock) BreakInfo() block.BreakInfo {
	return block.BreakInfo{
		Hardness:    1.5,
		Harvestable: func(item.Tool) bool { return false },
		Effective:   func(item.Tool) bool { return false },
		Drops:       func(item.Tool, []item.Enchantment) []item.Stack { return nil },
	}
}

// The client reads the component as seconds to destroy, so sending the hardness through
// makes every custom block break several times too fast.
func TestComponents_DestructibleByMiningIsSeconds(t *testing.T) {
	components := Components("test:breakable", breakableBlock{}, 10000)["components"].(map[string]any)

	raw, ok := components["minecraft:destructible_by_mining"]
	if !ok {
		t.Fatal("breakable block has no destructible_by_mining component")
	}
	value := raw.(map[string]any)["value"].(float32)
	// 1.5 hardness, unharvestable by hand: 1.5 * 100 ticks.
	if value != 7.5 {
		t.Fatalf("destructible_by_mining = %v, want 7.5 seconds (hardness is 1.5)", value)
	}
}

// unbreakableBlock is a custom block with the negative hardness Bedrock gives blocks that
// mining never destroys.
type unbreakableBlock struct{ breakableBlock }

func (unbreakableBlock) EncodeBlock() (string, map[string]any) { return "test:unbreakable", nil }

func (unbreakableBlock) BreakInfo() block.BreakInfo {
	info := breakableBlock{}.BreakInfo()
	info.Hardness = -1
	return info
}

// A negative hardness has to reach the client as its own -1 sentinel: scaling it into seconds
// like a normal hardness would send a duration the client cannot act on.
func TestComponents_DestructibleByMiningUnbreakable(t *testing.T) {
	components := Components("test:unbreakable", unbreakableBlock{}, 10001)["components"].(map[string]any)

	raw, ok := components["minecraft:destructible_by_mining"]
	if !ok {
		t.Fatal("unbreakable block has no destructible_by_mining component")
	}
	if value := raw.(map[string]any)["value"].(float32); value != -1 {
		t.Fatalf("destructible_by_mining = %v, want -1", value)
	}
}

func TestComponents_BlockTags(t *testing.T) {
	tags := []string{"minecraft:is_pickaxe_item_destructible", "minecraft:stone"}
	components := Components("test:tagged", taggedBlock{tags: tags}, 10000)

	raw, ok := components["blockTags"]
	if !ok {
		t.Fatal("tagged block has no blockTags")
	}
	// The client decodes the field as a list, so it is encoded as []any rather than a
	// typed slice.
	got, ok := raw.([]any)
	if !ok {
		t.Fatalf("blockTags = %T, want []any", raw)
	}
	if len(got) != len(tags) {
		t.Fatalf("blockTags = %d entries, want %d", len(got), len(tags))
	}
	for i, tag := range tags {
		if got[i] != tag {
			t.Errorf("blockTags[%d] = %v, want %s", i, got[i], tag)
		}
	}
}

func TestComponents_NoBlockTagsWithoutTagged(t *testing.T) {
	components := Components("test:untagged", untaggedBlock{}, 10000)
	if _, ok := components["blockTags"]; ok {
		t.Fatal("a block without tags must not carry a blockTags field")
	}
}

// An empty tag slice must be treated as no tags: sending an empty list would tell the
// client the block has no valid tools rather than leaving it to vanilla defaults.
func TestComponents_EmptyTagsAreOmitted(t *testing.T) {
	components := Components("test:tagged", taggedBlock{}, 10000)
	if _, ok := components["blockTags"]; ok {
		t.Fatal("an empty tag slice must not produce a blockTags field")
	}
}

func TestComponents_GeometryCulling(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		properties customblock.Properties
		identifier string
	}{
		{
			name:       "default cube",
			properties: customblock.Properties{Cube: true},
			identifier: "minecraft:geometry.full_block",
		},
		{
			name:       "custom geometry",
			properties: customblock.Properties{Geometry: "geometry.test.block"},
			identifier: "geometry.test.block",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.properties.GeometryCulling = "test:shared_faces"
			test.properties.GeometryCullingLayer = "test_xray"
			components := componentsFromProperties("test:block", test.properties)
			geometry := components["minecraft:geometry"].(map[string]any)
			if got := geometry["identifier"]; got != test.identifier {
				t.Errorf("identifier = %v, want %s", got, test.identifier)
			}
			if got := geometry["culling"]; got != "test:shared_faces" {
				t.Errorf("culling = %v, want test:shared_faces", got)
			}
			if got := geometry["culling_layer"]; got != "test_xray" {
				t.Errorf("culling_layer = %v, want test_xray", got)
			}
		})
	}
}

// orientedBlock is a custom block with both placement traits.
type orientedBlock struct{}

func (orientedBlock) EncodeBlock() (string, map[string]any) { return "test:oriented", nil }
func (orientedBlock) Model() world.BlockModel               { return nil }
func (orientedBlock) Hash() (uint64, uint64)                { return 0, 0 }

func (orientedBlock) Properties() customblock.Properties {
	return customblock.Properties{Cube: true}
}

func (orientedBlock) Traits() []customblock.Trait {
	return []customblock.Trait{
		customblock.PlacementDirection{FacingDirection: true, YRotationOffset: 180},
		customblock.PlacementPosition{BlockFace: true},
	}
}

// Traits are listed placement_position first whatever their declared order, each with the fields
// vanilla sends for it.
func TestComponents_Traits(t *testing.T) {
	components := Components("test:oriented", orientedBlock{}, 10000)
	want := []map[string]any{
		{
			"name": "minecraft:placement_position",
			"enabled_states": map[string]any{
				"block_face":    uint8(1),
				"vertical_half": uint8(0),
			},
		},
		{
			"name": "minecraft:placement_direction",
			"enabled_states": map[string]any{
				"cardinal_direction":            uint8(0),
				"facing_direction":              uint8(1),
				"corner_and_cardinal_direction": uint8(0),
				"sixteen_way_rotation":          uint8(0),
			},
			"y_rotation_offset":     float32(180),
			"blocks_to_corner_with": []any{},
		},
	}
	if got := components["traits"]; !reflect.DeepEqual(got, want) {
		t.Errorf("traits = %#v, want %#v", got, want)
	}
}

// invalidTraitBlock is a custom block whose traits vanilla rejects.
type invalidTraitBlock struct{ orientedBlock }

func (invalidTraitBlock) Traits() []customblock.Trait {
	return []customblock.Trait{customblock.PlacementPosition{}}
}

func TestComponents_InvalidTraitsPanic(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Components did not panic")
		}
	}()
	Components("test:invalid", invalidTraitBlock{}, 10000)
}

func TestComponents_NoTraitsWithoutTraited(t *testing.T) {
	components := Components("test:untagged", untaggedBlock{}, 10000)
	if _, ok := components["traits"]; ok {
		t.Fatal("a block without traits must not carry a traits field")
	}
}

// The encoded definition, sent through NBT, gives a client registry the trait states: the
// same states the server must register the block in.
func TestComponents_TraitStatesReachClientRegistry(t *testing.T) {
	data, err := nbt.Marshal(Components("test:oriented", orientedBlock{}, 10000))
	if err != nil {
		t.Fatalf("marshal components: %v", err)
	}
	var properties map[string]any
	if err := nbt.Unmarshal(data, &properties); err != nil {
		t.Fatalf("unmarshal components: %v", err)
	}
	registry, err := world.NewCustomBlockRegistry([]protocol.BlockEntry{{Name: "test:oriented", Properties: properties}})
	if err != nil {
		t.Fatalf("NewCustomBlockRegistry: %v", err)
	}
	for _, state := range []map[string]any{
		{"minecraft:facing_direction": "up", "minecraft:block_face": "north"},
		{"minecraft:facing_direction": "west", "minecraft:block_face": "down"},
	} {
		// StateToRuntimeID falls back to the block's first state, so the state found is checked.
		rid, _ := registry.StateToRuntimeID("test:oriented", state)
		if _, got, _ := registry.RuntimeIDToState(rid); !reflect.DeepEqual(got, state) {
			t.Errorf("state %v not registered, found %v", state, got)
		}
	}
}

// Bone visibility is a compound from bone name to a plain Molang string, constants written with six
// decimals, as vanilla sends it.
func TestComponents_BoneVisibility(t *testing.T) {
	t.Parallel()

	components := componentsFromProperties("test:cable", customblock.Properties{
		Geometry: "geometry.test.cable",
		BoneVisibility: map[string]string{
			"north": "q.block_state('test:north')",
			"core":  "!q.block_state('test:straight')",
			"on":    "1",
			"off":   " 0.0",
		},
	})
	geometry := components["minecraft:geometry"].(map[string]any)
	want := map[string]any{
		"north": "q.block_state('test:north')",
		"core":  "!q.block_state('test:straight')",
		"on":    "1.000000",
		"off":   "0.000000",
	}
	if got := geometry["bone_visibility"]; !reflect.DeepEqual(got, want) {
		t.Errorf("bone_visibility = %#v, want %#v", got, want)
	}

	data, err := nbt.Marshal(components)
	if err != nil {
		t.Fatalf("marshal components: %v", err)
	}
	var decoded map[string]any
	if err := nbt.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal components: %v", err)
	}
	if got := decoded["minecraft:geometry"].(map[string]any)["bone_visibility"]; !reflect.DeepEqual(got, want) {
		t.Errorf("bone_visibility through NBT = %#v, want %#v", got, want)
	}
}

// Vanilla rejects bone visibility on the built-in full block and cross geometries.
func TestComponents_BoneVisibilityOnBuiltInGeometryPanics(t *testing.T) {
	t.Parallel()

	for _, props := range []customblock.Properties{
		{Cube: true},
		{Geometry: "minecraft:geometry.full_block"},
		{Geometry: "minecraft:geometry.cross"},
	} {
		props.BoneVisibility = map[string]string{"a": "1"}
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("componentsFromProperties(%+v) did not panic", props)
				}
			}()
			componentsFromProperties("test:block", props)
		}()
	}
}

func TestComponents_NoBoneVisibilityByDefault(t *testing.T) {
	t.Parallel()

	components := componentsFromProperties("test:block", customblock.Properties{Geometry: "geometry.test.block"})
	if _, ok := components["minecraft:geometry"].(map[string]any)["bone_visibility"]; ok {
		t.Fatal("geometry without bone visibility must not carry the field")
	}
}

// Definitions are sent at the latest Molang version, 13 (1.21.100), as vanilla's are
// (data_driven_blocks.nbt); 1.26.50 clients reject a definition with anything newer.
func TestComponents_MolangVersion(t *testing.T) {
	components := Components("test:untagged", untaggedBlock{}, 10000)
	if got := components["molangVersion"]; got != int32(13) {
		t.Errorf("molangVersion = %#v, want int32(13)", got)
	}
}

// statefulBlock is a custom block with several properties.
type statefulBlock struct{}

func (statefulBlock) EncodeBlock() (string, map[string]any) { return "test:stateful", nil }
func (statefulBlock) Model() world.BlockModel               { return nil }
func (statefulBlock) Hash() (uint64, uint64)                { return 0, 0 }
func (statefulBlock) Permutations() []customblock.Permutation {
	return nil
}

func (statefulBlock) Properties() customblock.Properties {
	return customblock.Properties{Cube: true}
}

func (statefulBlock) States() map[string][]any {
	return map[string][]any{"test:c": {false, true}, "test:a": {false, true}, "test:b": {false, true}}
}

// The client adds properties to the block in the order of the list, which decides the order of
// its runtime IDs, so the list is sorted by name rather than left to map iteration.
func TestComponents_PropertiesSortedByName(t *testing.T) {
	for range 20 {
		properties := Components("test:stateful", statefulBlock{}, 10000)["properties"].([]map[string]any)
		var names []any
		for _, property := range properties {
			names = append(names, property["name"])
		}
		if want := []any{"test:a", "test:b", "test:c"}; !reflect.DeepEqual(names, want) {
			t.Fatalf("property names = %v, want %v", names, want)
		}
	}
}

// The client applies a block's permutations in list order, the later one's components winning
// where two hold, so they keep the order the block lists them in; a repeated condition adds its
// components to the first permutation with it.
func TestBuilder_PermutationsKeepTheirOrder(t *testing.T) {
	builder := NewComponentBuilder("test:ordered", nil, 1)
	conditions := []string{"q.block_state('a') == 3", "q.block_state('a') == 1", "q.block_state('a') == 2", "true", "false"}
	for i, condition := range conditions {
		builder.AddPermutation(condition, map[string]any{"index": i})
	}
	builder.AddPermutation(conditions[1], map[string]any{"again": true})
	permutations := builder.Construct()["permutations"].([]map[string]any)
	if len(permutations) != len(conditions) {
		t.Fatalf("%d permutations, want %d", len(permutations), len(conditions))
	}
	for i, permutation := range permutations {
		if permutation["condition"] != conditions[i] || permutation["components"].(map[string]any)["index"] != i {
			t.Fatalf("permutation %d = %v, want condition %q", i, permutation, conditions[i])
		}
	}
	if permutations[1]["components"].(map[string]any)["again"] != true {
		t.Errorf("the repeated condition's components went elsewhere: %v", permutations)
	}
}
