package world

import (
	"testing"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

func TestNewNetworkBlockRegistry_NegotiatedVanillaBlocks(t *testing.T) {
	DefaultBlockRegistry.Finalize()
	all := make([]protocol.BlockEntry, 0, len(DataDrivenBlocks()))
	for _, block := range DataDrivenBlocks() {
		all = append(all, protocol.BlockEntry{Name: block.Name, Properties: block.Components})
	}
	for _, tc := range []struct {
		name    string
		entries []protocol.BlockEntry
	}{
		{"none", nil},
		{"one", all[:1]},
		{"all", all},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry, err := NewNetworkBlockRegistry(tc.entries)
			if err != nil {
				t.Fatal(err)
			}
			advertised := map[string]bool{}
			for _, entry := range tc.entries {
				advertised[entry.Name] = true
			}
			for _, block := range DataDrivenBlocks() {
				if _, ok := registry.StateToRuntimeID(block.Name, nil); ok != advertised[block.Name] {
					t.Errorf("%s present = %t, advertised = %t", block.Name, ok, advertised[block.Name])
				}
			}
			if tc.name == "all" {
				for rid, block := range DefaultBlockRegistry.Blocks() {
					name, properties := block.EncodeBlock()
					if got, ok := registry.StateToRuntimeID(name, properties); !ok || got != uint32(rid) {
						t.Fatalf("%s runtime ID = (%d, %t), want %d", name, got, ok, rid)
					}
				}
			}
		})
	}
}

func TestNewNetworkBlockRegistry_BuiltInRuntimeIDsWithoutOptionalBlocks(t *testing.T) {
	registry, err := NewNetworkBlockRegistry([]protocol.BlockEntry{{Name: "lunar:abgbzvnw0"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		id   uint32
	}{
		{"minecraft:oak_log", 1807}, {"minecraft:stone", 3219},
		{"minecraft:grass_block", 15228}, {"minecraft:barrier", 15778}, {"minecraft:air", 17713},
	} {
		if got, ok := registry.StateToRuntimeID(tc.name, nil); !ok || got != tc.id {
			t.Errorf("%s runtime ID = (%d, %t), want %d", tc.name, got, ok, tc.id)
		}
	}
	custom, ok := registry.StateToRuntimeID("lunar:abgbzvnw0", nil)
	if !ok {
		t.Fatal("custom state missing")
	}
	hash, ok := registry.RuntimeIDToHash(custom)
	if !ok {
		t.Fatal("custom state hash missing")
	}
	if back, ok := registry.HashToRuntimeID(hash); !ok || back != custom {
		t.Fatal("custom hash did not round-trip")
	}
}

func TestNewNetworkBlockRegistry_InheritedCustomBlocks(t *testing.T) {
	baseline, err := NewNetworkBlockRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := NewBlockRegistry().(*BasicBlockRegistry)
	const count = 4 * 2 * 2 * 3
	for i := count - 1; i >= 0; i-- {
		seed.RegisterBlock(orderedState(i))
	}
	seed.RegisterBlockState(BlockState{Name: "test:unregistered", Properties: map[string]any{"test:variant": int32(99)}})
	seed.Finalize()
	original := DefaultBlockRegistry
	DefaultBlockRegistry = seed
	t.Cleanup(func() { DefaultBlockRegistry = original })

	t.Run("unadvertised", func(t *testing.T) {
		registry, err := NewNetworkBlockRegistry(nil)
		if err != nil {
			t.Fatal(err)
		}
		if registry.BlockCount() != baseline.BlockCount() {
			t.Fatalf("inherited custom states changed palette size: got %d, want %d", registry.BlockCount(), baseline.BlockCount())
		}
		if len(registry.CustomBlocks()) != 0 {
			t.Fatal("unadvertised custom implementation retained")
		}
		for rid, block := range baseline.Blocks() {
			name, properties := block.EncodeBlock()
			if got, ok := registry.StateToRuntimeID(name, properties); !ok || got != uint32(rid) {
				t.Fatalf("%s runtime ID = (%d, %t), want %d", name, got, ok, rid)
			}
		}
	})
	t.Run("advertised", func(t *testing.T) {
		registry, err := NewNetworkBlockRegistry([]protocol.BlockEntry{
			{
				Name: "test:ordered",
				Properties: map[string]any{
					"properties": []any{
						map[string]any{"name": "test:a", "enum": []any{false, true}},
						map[string]any{"name": "test:b", "enum": []any{"x", "y", "z"}},
					},
					"traits": []any{
						map[string]any{"name": "minecraft:placement_position", "enabled_states": map[string]any{"vertical_half": uint8(1)}},
						map[string]any{"name": "minecraft:placement_direction", "enabled_states": map[string]any{"cardinal_direction": uint8(1)}},
					},
				},
			},
			{Name: "test:unregistered", Properties: map[string]any{
				"properties": []any{map[string]any{"name": "test:variant", "enum": []any{int32(0), int32(1)}}},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(registry.CustomBlocks()) != 1 || registry.CustomBlocks()["test:ordered"] == nil {
			t.Fatal("advertised custom implementation missing")
		}
		first := registry.BlockRuntimeID(orderedState(0))
		for i := range count {
			if got, ok := registry.BlockByRuntimeID(first + uint32(i)); !ok || got != orderedState(i) {
				t.Fatalf("custom implementation at offset %d = %#v, want %#v", i, got, orderedState(i))
			}
		}
		if got, want := registry.BlockCount(), baseline.BlockCount()+count+2; got != want {
			t.Fatalf("advertised palette has %d states, want %d without stale inherited states", got, want)
		}
		for _, variant := range []int32{0, 1} {
			rid, ok := registry.StateToRuntimeID("test:unregistered", map[string]any{"test:variant": variant})
			if !ok {
				t.Fatalf("advertised state %d missing", variant)
			}
			if _, properties, _ := registry.RuntimeIDToState(rid); properties["test:variant"] != variant {
				t.Fatalf("advertised state %d resolved to %v", variant, properties)
			}
		}
	})
	if _, ok := seed.StateToRuntimeID("test:unregistered", map[string]any{"test:variant": int32(99)}); !ok || len(seed.CustomBlocks()) != 1 {
		t.Fatal("network registry construction mutated the default registry")
	}
}
