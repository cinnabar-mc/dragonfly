package world

import (
	"github.com/df-mc/dragonfly/server/block/cube"
)

// loadedSource is a BlockSource reading from the chunks a World has loaded. Positions in an unloaded
// chunk or outside the World's Range read as air.
type loadedSource struct{ w *World }

// Block ...
func (s loadedSource) Block(pos cube.Pos) Block {
	b, _ := s.w.blockLoaded(pos)
	return b
}

// derivedBlock is a StateDeriver found in a chunk at a position.
type derivedBlock struct {
	pos cube.Pos
	b   StateDeriver
}

// deriveStates recalculates the derived states of the chunk at centre and of the chunks around it, so
// that a chunk that was missing neighbours earlier is upgraded once the chunk completing it is loaded.
func (w *World) deriveStates(centre ChunkPos) {
	for x := int32(-1); x <= 1; x++ {
		for z := int32(-1); z <= 1; z++ {
			pos := ChunkPos{centre[0] + x, centre[1] + z}
			if c, ok := w.chunks[pos]; ok {
				if c.LegacyStates() {
					w.deriveChunkStates(pos, c)
				} else {
					w.applyDerivedStates(c, w.derivedBlocks(pos, c, &centre))
				}
			}
		}
	}
}

// deriveChunkStates recalculates the derived states of the chunk at pos. Blocks on the edge of a chunk
// derive their state partly from the chunk next to it, so the chunk is left marked and untouched until
// every chunk around it is loaded.
func (w *World) deriveChunkStates(pos ChunkPos, c *Column) {
	for x := int32(-1); x <= 1; x++ {
		for z := int32(-1); z <= 1; z++ {
			if _, ok := w.chunks[ChunkPos{pos[0] + x, pos[1] + z}]; !ok {
				return
			}
		}
	}
	found := w.derivedBlocks(pos, c, nil)
	c.MarkStatesUpgraded()
	w.applyDerivedStates(c, found)
}

// applyDerivedStates updates derived appearance without changing pending tick deadlines.
func (w *World) applyDerivedStates(c *Column, found []derivedBlock) {
	if len(found) == 0 {
		return
	}

	src := loadedSource{w: w}
	for _, f := range found {
		derived := f.b.DeriveState(f.pos, src)
		if derived == Block(f.b) {
			continue
		}
		// Written straight into the chunk rather than through Tx.SetBlock, which would run the
		// neighbour updates of a state that was only ever wrong in memory.
		c.SetBlock(uint8(f.pos[0]), int16(f.pos[1]), uint8(f.pos[2]), 0, w.conf.Blocks.BlockRuntimeID(derived))
		w.scheduledUpdates.replaceState(w.conf.Blocks, f.pos, f.b, derived)
		c.modified = true

		// A chunk whose neighbours arrived after it was sent is upgraded while viewers already hold
		// the state it was loaded with, so they are told about the block themselves.
		for _, v := range c.viewers {
			v.ViewBlockUpdate(f.pos, derived, 0)
		}
	}
}

// derivedBlocks returns every StateDeriver in the chunk at pos. Sub chunks are filtered on their
// palette first, so that one holding none of them is skipped without reading any block in it. When
// facing is non-nil, only derivers on the chunk boundary shared with that chunk are returned, so a
// neighbour load refreshes just the edge it can affect instead of the whole chunk.
func (w *World) derivedBlocks(pos ChunkPos, c *Column, facing *ChunkPos) []derivedBlock {
	var found []derivedBlock
	baseX, baseZ := int(pos[0])<<4, int(pos[1])<<4
	for index, sub := range c.Sub() {
		if sub.Empty() {
			continue
		}
		storage := sub.Layer(0)
		palette := storage.Palette()

		// derivers maps palette indices holding a StateDeriver to that block, and stays nil if none do.
		var derivers map[uint16]StateDeriver
		for i := 0; i < palette.Len(); i++ {
			b, ok := w.conf.Blocks.BlockByRuntimeID(palette.Value(uint16(i)))
			if !ok {
				continue
			}
			if d, ok := b.(StateDeriver); ok {
				if derivers == nil {
					derivers = make(map[uint16]StateDeriver, 4)
				}
				derivers[uint16(i)] = d
			}
		}
		if derivers == nil {
			continue
		}

		baseY := int(c.SubY(int16(index)))
		for x := byte(0); x < 16; x++ {
			for z := byte(0); z < 16; z++ {
				if facing != nil && !touchesLoadedBoundary(x, z, pos, *facing) {
					continue
				}
				for y := byte(0); y < 16; y++ {
					if d, ok := derivers[storage.PaletteIndex(x, y, z)]; ok {
						found = append(found, derivedBlock{
							pos: cube.Pos{baseX + int(x), baseY + int(y), baseZ + int(z)},
							b:   d,
						})
					}
				}
			}
		}
	}
	return found
}

// touchesLoadedBoundary reports whether the column (x, z) of chunk pos lies on the boundary shared
// with the chunk at facing. When facing is pos itself, every edge column qualifies, since any of the
// chunk's neighbours may have just become available.
func touchesLoadedBoundary(x, z byte, pos, facing ChunkPos) bool {
	dx, dz := facing[0]-pos[0], facing[1]-pos[1]
	if dx == 0 && dz == 0 {
		return x == 0 || x == 15 || z == 0 || z == 15
	}
	return axisTouches(dx, x) && axisTouches(dz, z)
}

// axisTouches reports whether coord sits on the edge of a chunk facing another chunk one step away
// in direction d along that axis. d == 0 means the chunks share the whole axis, so any coord does.
func axisTouches(d int32, coord byte) bool {
	switch d {
	case -1:
		return coord == 0
	case 1:
		return coord == 15
	default:
		return true
	}
}
