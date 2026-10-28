package chunk

import (
	"bytes"
	"maps"
	"reflect"
	"testing"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
)

// upgradedChestRegistry accepts only the correctly upgraded north-facing chest.
type upgradedChestRegistry struct{ testBlockRegistry }

// StateToRuntimeID checks the properties produced by the real upgrade schemas.
func (upgradedChestRegistry) StateToRuntimeID(name string, properties map[string]any) (uint32, bool) {
	return 1, name == "minecraft:chest" && maps.Equal(properties, map[string]any{"minecraft:cardinal_direction": "north"})
}

func TestLegacyDiskDecodePreservesMappings(t *testing.T) {
	before := make(map[legacyBlockEntry]blockEntry, len(legacyMappings))
	for key, entry := range legacyMappings {
		entry.State = maps.Clone(entry.State)
		before[key] = entry
	}
	var sub bytes.Buffer
	sub.Write([]byte{8, 1, 0}) // One storage with a single legacy palette entry.
	if err := nbt.NewEncoderWithEncoding(&sub, nbt.LittleEndian).Encode(map[string]any{
		"name": "minecraft:chest", "val": int16(2),
	}); err != nil {
		t.Fatal(err)
	}
	c, err := DiskDecode(upgradedChestRegistry{}, SerialisedData{SubChunks: [][]byte{sub.Bytes()}}, cube.Range{0, 15})
	if err != nil {
		t.Fatal(err)
	}
	if c.Block(1, 1, 1, 0) != 1 || !c.LegacyStates() {
		t.Fatal("legacy chest did not decode to its upgraded state")
	}
	if !reflect.DeepEqual(legacyMappings, before) {
		key := legacyBlockEntry{Name: "minecraft:chest", Meta: 2}
		t.Fatalf("legacy decoding mutated shared mappings: chest properties = %v, want %v", legacyMappings[key].State, before[key].State)
	}
}

func TestBlockStateDecodePreservesInputProperties(t *testing.T) {
	properties := map[string]any{"facing_direction": int32(2)}
	input := map[string]any{
		"name": "minecraft:chest", "version": int32(18040335), "states": properties,
	}
	before := CloneBlockEntityData(input)
	rid, err := (BlockPaletteEncoding{Blocks: upgradedChestRegistry{}}).DecodeBlockState(input)
	if err != nil || rid != 1 {
		t.Fatalf("chest decode = %d, %v", rid, err)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatalf("decoding mutated caller input: %v, want %v", input, before)
	}
}
