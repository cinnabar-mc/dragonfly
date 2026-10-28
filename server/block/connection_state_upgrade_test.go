package block

import (
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/chunk"
	"testing"
)

type connectionFireProvider struct {
	world.NopProvider
	central *chunk.Column
	saved   *chunk.Column
}

// LoadColumn provides legacy wall fire and otherwise empty terrain.
func (p *connectionFireProvider) LoadColumn(pos world.ChunkPos, _ world.Dimension) (*chunk.Column, error) {
	if pos == (world.ChunkPos{}) {
		return p.central, nil
	}
	return nil, world.ErrColumnNotFound
}

// StoreColumn captures the active scheduled ticks.
func (p *connectionFireProvider) StoreColumn(pos world.ChunkPos, _ world.Dimension, col *chunk.Column) error {
	if pos == (world.ChunkPos{}) {
		p.saved = col
	}
	return nil
}
func TestLegacyFireDerivationPreservesScheduledTick(t *testing.T) {
	br := world.DefaultBlockRegistry
	br.Finalize()
	c := chunk.New(br, world.Overworld.Range())
	pos := cube.Pos{8, 64, 8}
	c.SetBlock(8, 64, 8, 0, br.BlockRuntimeID(Fire{}))
	c.SetBlock(9, 64, 8, 0, br.BlockRuntimeID(Planks{}))
	c.MarkLegacyStates()
	p := &connectionFireProvider{central: &chunk.Column{Chunk: c, ScheduledBlocks: []chunk.ScheduledBlockUpdate{{Pos: pos, Block: br.BlockRuntimeID(Fire{}), Tick: 2}}}}
	w := world.Config{Synchronous: true, Provider: p, RandomTickSpeed: -1}.New()
	defer w.Close()
	runWorld(w, func(tx *world.Tx) {
		for x := -1; x <= 1; x++ {
			for z := -1; z <= 1; z++ {
				tx.Block(cube.Pos{x * 16, 64, z * 16})
			}
		}
		f := tx.Block(pos).(Fire)
		if f.Connections.Uint8() == 0 {
			t.Fatal("setup: not wall fire")
		}
	})
	for range 3 {
		w.AdvanceTick()
	}
	w.Save()
	if p.saved == nil || len(p.saved.ScheduledBlocks) == 0 {
		t.Fatal("fire's scheduled tick disappeared during legacy derivation")
	}
}

type connectionWireProvider struct {
	world.NopProvider
	cols map[world.ChunkPos]*chunk.Column
}

// LoadColumn supplies a neighboring saved wire without making it resident early.
func (p *connectionWireProvider) LoadColumn(pos world.ChunkPos, _ world.Dimension) (*chunk.Column, error) {
	if c, ok := p.cols[pos]; ok {
		return c, nil
	}
	return nil, world.ErrColumnNotFound
}
func TestWireConnectionsRefreshWhenAdjacentChunkLoads(t *testing.T) {
	br := world.DefaultBlockRegistry
	br.Finalize()
	left, right := chunk.New(br, world.Overworld.Range()), chunk.New(br, world.Overworld.Range())
	left.SetBlock(15, 63, 8, 0, br.BlockRuntimeID(Stone{}))
	right.SetBlock(0, 63, 8, 0, br.BlockRuntimeID(Stone{}))
	right.SetBlock(0, 64, 8, 0, br.BlockRuntimeID(RedstoneWire{Connections: WireConnections{east: SideWireConnection(), west: SideWireConnection()}}))
	p := &connectionWireProvider{cols: map[world.ChunkPos]*chunk.Column{{0, 0}: {Chunk: left}, {1, 0}: {Chunk: right}}}
	w := world.Config{Synchronous: true, Provider: p, RandomTickSpeed: -1}.New()
	defer w.Close()
	pos := cube.Pos{15, 64, 8}
	runWorld(w, func(tx *world.Tx) {
		tx.Block(pos)
		if _, loaded := tx.BlockLoaded(cube.Pos{16, 64, 8}); loaded {
			t.Fatal("setup: right chunk already resident")
		}
		wire := RedstoneWire{}
		wire.Connections = redstoneWireConnections(pos, loadedSource{tx: tx})
		tx.SetBlock(pos, wire, nil)
	})
	for range 4 {
		w.AdvanceTick()
	}
	runWorld(w, func(tx *world.Tx) {
		if _, loaded := tx.BlockLoaded(cube.Pos{16, 64, 8}); !loaded {
			t.Fatal("setup: right chunk did not load")
		}
		actual := tx.Block(pos).(RedstoneWire).Connections
		expected := redstoneWireConnections(pos, loadedSource{tx: tx})
		if actual != expected {
			t.Fatalf("wire retained missing-neighbor shape: actual=%v expected=%v", actual, expected)
		}
	})
}
