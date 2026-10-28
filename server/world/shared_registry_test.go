package world

import (
	"bytes"
	"testing"

	"github.com/cinnabar-mc/protocolgen/generated/data"
	sharedblock "github.com/cinnabar-mc/protocolgen/generated/data/block"
	shareditem "github.com/cinnabar-mc/protocolgen/generated/data/item"
	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// TestSharedCatalogTargetMatchesCodec rejects data and packet codecs for different targets.
func TestSharedCatalogTargetMatchesCodec(t *testing.T) {
	if data.MinecraftVersion != protocol.CurrentVersion || data.ProtocolVersion != protocol.CurrentProtocol {
		t.Fatalf("catalog target %s/%d does not match codecs %s/%d", data.MinecraftVersion, data.ProtocolVersion, protocol.CurrentVersion, protocol.CurrentProtocol)
	}
}

// TestSharedPaletteCoversEveryState checks the full join between network states,
// scalar properties, and available collision and outline geometry.
func TestSharedPaletteCoversEveryState(t *testing.T) {
	palette := NewBlockRegistry()
	palette.Finalize()
	if palette.BlockCount() != sharedblock.StateCount() {
		t.Fatalf("embedded palette has %d states, catalog count %d", palette.BlockCount(), sharedblock.StateCount())
	}
	for runtimeID, block := range palette.Blocks() {
		name, _ := block.EncodeBlock()
		hash, ok := palette.RuntimeIDToHash(uint32(runtimeID))
		if !ok {
			t.Errorf("%s has no network hash", name)
			continue
		}
		state, ok := sharedblock.StateByHash(hash)
		if !ok {
			t.Errorf("%s hash %d has no shared state", name, hash)
			continue
		}
		definition, ok := sharedblock.BlockAt(state.Block)
		if !ok || definition.Name != name {
			t.Errorf("hash %d resolves to %s, want %s", hash, definition.Name, name)
		}
		properties, ok := sharedblock.PropertiesAt(state.Properties)
		if !ok {
			t.Errorf("%s has no shared properties", name)
			continue
		}
		// Zero explicitly marks an unavailable shape rather than an invalid index.
		if _, available := sharedblock.Shape(properties.CollisionShape); properties.CollisionShape != 0 && !available {
			t.Errorf("%s hash %d has an invalid collision shape index", name, hash)
		}
		if _, available := sharedblock.Shape(properties.OutlineShape); properties.OutlineShape != 0 && !available {
			t.Errorf("%s hash %d has an invalid outline shape index", name, hash)
		}
		if _, ok := unknownBlockProps[name]; !ok {
			t.Errorf("%s has no shared fallback properties", name)
		}
	}
}

// TestSharedItemsMatchRegistry checks both directions of the identity projection.
func TestSharedItemsMatchRegistry(t *testing.T) {
	entries := VanillaItemEntries()
	if len(entries) != len(shareditem.RuntimeItems()) {
		t.Fatalf("item counts differ: embedded entries=%d catalog=%d", len(entries), len(shareditem.RuntimeItems()))
	}
	for _, definition := range shareditem.RuntimeItems() {
		entry, ok := entries[definition.Name]
		if !ok || entry.RuntimeID != definition.RuntimeID || entry.ComponentBased != definition.ComponentBased || entry.Version != definition.Version {
			t.Errorf("%s identity differs between catalog and registry: %+v", definition.Name, entry)
		}
		if entry.Data == nil {
			t.Errorf("%s lost its component compound", definition.Name)
		}
	}
}

// TestDecodeBlockStatesRejectsTruncation prevents a damaged final record from
// silently reducing the registry to the preceding complete states.
func TestDecodeBlockStatesRejectsTruncation(t *testing.T) {
	data := bytes.Clone(blockStateData)
	for _, damaged := range [][]byte{data[:len(data)-1], append(data, 0xff)} {
		if _, err := decodeBlockStates(damaged); err == nil {
			t.Fatal("accepted a damaged block state stream")
		}
	}
}
