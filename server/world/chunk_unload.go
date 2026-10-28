package world

import (
	"slices"

	"github.com/df-mc/dragonfly/server/world/chunk"
)

// pendingChunkSave retains terrain for owner-side reloads until its provider write finishes.
// Only the existing autosave goroutine writes unused columns, one at a time.
type pendingChunkSave struct {
	pos    ChunkPos
	column *chunk.Column
}

// closeUnusedChunks saves unused columns from the autosave goroutine. Each owner
// transaction only captures or retires a column; provider I/O runs between transactions.
func (w *World) closeUnusedChunks() {
	w.saveMu.Lock()
	defer w.saveMu.Unlock()
	if w.closed.Load() {
		return
	}
	var positions []ChunkPos
	<-w.exec(func(tx *Tx) {
		for pos, c := range w.chunks {
			if len(c.viewers) == 0 {
				positions = append(positions, pos)
			}
		}
	})
	for _, pos := range positions {
		if w.closed.Load() {
			return
		}
		var column *chunk.Column
		<-w.exec(func(tx *Tx) {
			c, ok := w.chunks[pos]
			if !ok || w.closed.Load() || len(c.viewers) != 0 {
				return
			}
			if !w.conf.ReadOnly && c.modified {
				c.Compact()
				snapshot := cloneSaveColumn(w.columnTo(c, pos))
				w.unloading = &pendingChunkSave{pos: pos, column: snapshot}
				column = cloneSaveColumn(snapshot)
			}
			w.unloadChunk(tx, pos, c)
		})
		if column == nil {
			continue
		}
		err := w.conf.Provider.StoreColumn(pos, w.conf.Dim, column)
		<-w.exec(func(tx *Tx) {
			if err != nil {
				w.conf.Log.Error("save chunk: "+err.Error(), "X", pos[0], "Z", pos[1])
				// Keep failed terrain resident and dirty for a later sweep or
				// shutdown retry. A revived column already owns newer state.
				if _, ok := w.chunks[pos]; !ok {
					w.chunkFromPendingSave(pos)
				}
			}
			w.unloading = nil
		})
	}
}

// chunkFromPendingSave revives a detached snapshot without reading stale provider
// data or waiting for an in-flight write. The caller must be on the world owner.
func (w *World) chunkFromPendingSave(pos ChunkPos) (*Column, bool) {
	if pending, ok := w.pendingColumn(pos); ok {
		column := cloneSaveColumn(pending)
		// Save revived terrain again, including edits made while the older
		// snapshot is still being written or after that write failed.
		column.Generated = true
		return w.addChunk(pos, column), true
	}
	return nil, false
}

// pendingColumn returns retained terrain for a position with no resident column.
// Callers must check resident terrain first and run on the world owner. The
// returned snapshot is immutable; revival requires an independent copy.
func (w *World) pendingColumn(pos ChunkPos) (*chunk.Column, bool) {
	if pending := w.unloading; pending != nil && pending.pos == pos {
		return pending.column, true
	}
	return nil, false
}

// cloneSaveColumn separates terrain and nested NBT ownership for persistence and revival.
func cloneSaveColumn(column *chunk.Column) *chunk.Column {
	copy := *column
	copy.Chunk = column.Chunk.Clone()
	copy.Entities = slices.Clone(column.Entities)
	for i := range copy.Entities {
		copy.Entities[i].Data = chunk.CloneBlockEntityData(copy.Entities[i].Data)
	}
	copy.BlockEntities = slices.Clone(column.BlockEntities)
	for i := range copy.BlockEntities {
		copy.BlockEntities[i].Data = chunk.CloneBlockEntityData(copy.BlockEntities[i].Data)
	}
	copy.ScheduledBlocks = slices.Clone(column.ScheduledBlocks)
	return &copy
}
