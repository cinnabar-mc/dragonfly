package world

import (
	"slices"
	"strings"

	"github.com/sandertv/gophertunnel/minecraft/protocol"
)

// NewNetworkBlockRegistry builds a finalized registry for a StartGame block palette.
// Vanilla data-driven blocks participate only when advertised, while built-in
// vanilla blocks always participate. Advertised custom implementations are
// retained; other custom states come from the advertised definitions.
// The returned registry is independent of DefaultBlockRegistry.
func NewNetworkBlockRegistry(entries []protocol.BlockEntry) (BlockRegistry, error) {
	DefaultBlockRegistry.Finalize()
	registry := NewBlockRegistry().(*BasicBlockRegistry)
	implementations := make(map[string]CustomBlock)
	omitted := make(map[string]struct{}, len(DataDrivenBlocks()))
	for _, block := range DataDrivenBlocks() {
		omitted[block.Name] = struct{}{}
	}
	for _, block := range registry.blocks {
		name, _ := block.EncodeBlock()
		if !strings.HasPrefix(name, "minecraft:") {
			omitted[name] = struct{}{}
		}
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, "minecraft:") {
			delete(omitted, entry.Name)
		}
		if block, ok := registry.customBlocks[entry.Name]; ok {
			implementations[entry.Name] = block
		}
	}
	clear(registry.customBlocks)
	for name := range omitted {
		delete(registry.blockProperties, name)
	}
	registry.blocks = slices.DeleteFunc(registry.blocks, func(block Block) bool {
		name, _ := block.EncodeBlock()
		_, skip := omitted[name]
		return skip
	})
	// Custom-state insertion uses this index before finalization rebuilds it.
	clear(registry.stateRuntimeIDs)
	for rid, block := range registry.blocks {
		name, properties := block.EncodeBlock()
		registry.stateRuntimeIDs[stateHash{name: name, properties: hashProperties(properties)}] = uint32(rid)
	}
	if err := AddCustomBlocks(registry, entries); err != nil {
		return nil, err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	// Sort the advertised state space before attaching local implementation metadata.
	registry.sortForNetworkLocked()
	for rid, block := range registry.blocks {
		name, properties := block.EncodeBlock()
		if _, custom := implementations[name]; !custom {
			continue
		}
		if implementation, ok := DefaultBlockRegistry.BlockByName(name, properties); ok {
			registry.blocks[rid] = implementation
		}
	}
	registry.customBlocks = implementations
	registry.finalizeLocked()
	return registry, nil
}
