package world

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world/chunk"
)

// searchUnloadProvider exposes missing or older terrain during a blocked store.
type searchUnloadProvider struct {
	*blockedUnloadProvider
	pos      ChunkPos
	previous *chunk.Column
	reads    atomic.Int32
}

// ConcurrentColumnReads permits reads of this fixture's immutable saved terrain.
func (*searchUnloadProvider) ConcurrentColumnReads() bool { return true }

// LoadColumn returns the old persisted column without waiting for the new store.
func (p *searchUnloadProvider) LoadColumn(pos ChunkPos, _ Dimension) (*chunk.Column, error) {
	if pos == p.pos {
		p.reads.Add(1)
		if p.previous != nil {
			return cloneSaveColumn(p.previous), nil
		}
	}
	return nil, ErrColumnNotFound
}

func TestBlocksWithinSeesDetachedTerrain(t *testing.T) {
	for _, saved := range []bool{false, true} {
		name := "missing"
		if saved {
			name = "older"
		}
		t.Run(name, func(t *testing.T) {
			p := &searchUnloadProvider{
				blockedUnloadProvider: &blockedUnloadProvider{entered: make(chan struct{}), release: make(chan struct{})},
				pos:                   ChunkPos{3, 4},
			}
			generator := &recordingChunkGenerator{}
			w := Config{Provider: p, Generator: generator, ChunkUnloadInterval: time.Hour, SaveInterval: -1, RandomTickSpeed: -1}.New()
			defer w.Close()
			portal, ok := w.conf.Blocks.BlockByName("minecraft:portal", map[string]any{"portal_axis": "x"})
			if !ok {
				t.Fatal("portal state is not registered")
			}
			rid := w.conf.Blocks.BlockRuntimeID(portal)
			oldPos, currentPos, newerPos := cube.Pos{49, 1, 65}, cube.Pos{50, 1, 66}, cube.Pos{49, 1, 66}
			if saved {
				previous := chunk.New(w.conf.Blocks, w.Range())
				previous.SetBlock(uint8(oldPos[0]), int16(oldPos[1]), uint8(oldPos[2]), 0, rid)
				p.previous = &chunk.Column{Chunk: previous}
			}
			<-w.exec(func(tx *Tx) {
				c := tx.chunk(p.pos)
				c.SetBlock(uint8(oldPos[0]), int16(oldPos[1]), uint8(oldPos[2]), 0, w.conf.Blocks.AirRuntimeID())
				c.SetBlock(uint8(currentPos[0]), int16(currentPos[1]), uint8(currentPos[2]), 0, rid)
				c.modified = true
			})
			unloaded := make(chan struct{})
			go func() { w.closeUnusedChunks(); close(unloaded) }()
			var release sync.Once
			unblock := func() { release.Do(func() { close(p.release) }) }
			defer func() { unblock(); <-unloaded }()
			select {
			case <-p.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("detached terrain did not reach storage")
			}
			task := w.Do(func(tx *Tx) {
				reads, generated := p.reads.Load(), len(generator.positions)
				if got := slices.Collect(tx.BlocksWithin(currentPos, 2, portal)); !slices.Equal(got, []cube.Pos{currentPos}) {
					t.Errorf("search during storage = %v, want current portal %v", got, currentPos)
				}
				if p.reads.Load() != reads {
					t.Error("search read stale provider terrain for the detached column")
				}
				if got := slices.Collect(tx.BlocksWithin(cube.Pos{82, 1, 66}, 1, portal)); len(got) != 0 {
					t.Errorf("missing-column search returned %v", got)
				}
				if len(w.chunks) != 0 || len(generator.positions) != generated {
					t.Error("search revived or generated terrain")
				}
				// Only now revive and edit. Resident terrain must outrank the
				// older detached copy while the same store is still blocked.
				c := tx.chunk(p.pos)
				c.SetBlock(uint8(currentPos[0]), int16(currentPos[1]), uint8(currentPos[2]), 0, w.conf.Blocks.AirRuntimeID())
				c.SetBlock(uint8(newerPos[0]), int16(newerPos[1]), uint8(newerPos[2]), 0, rid)
				c.viewers = []Viewer{NopViewer{}}
				if got := slices.Collect(tx.BlocksWithin(currentPos, 2, portal)); !slices.Equal(got, []cube.Pos{newerPos}) {
					t.Errorf("search after revival = %v, want newer portal %v", got, newerPos)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := task.Wait(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
