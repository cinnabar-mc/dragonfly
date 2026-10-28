package world

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/df-mc/dragonfly/server/world/chunk"
	"github.com/go-gl/mathgl/mgl64"
)

func TestLoaderMovementSkipsUnwantedQueuedGeneration(t *testing.T) {
	withQueuedChunkWorld(t, func(tx *Tx) {
		w := tx.World()
		g := &recordingChunkGenerator{}
		w.conf.Generator = g
		v := &chunkQueueViewer{}
		l := NewLoader(5, w, v)
		defer l.Close(tx)
		l.Load(tx, 32)
		l.Move(tx, mgl64.Vec3{16000, 0, 0})
		// Draining the obsolete work must not call the generator.
		finishQueuedChunks(w)
		if len(g.positions) != 0 || len(v.chunks) != 0 || l.queued.Load() != 0 {
			t.Fatal("obsolete requests generated, delivered, or retained an allowance")
		}
		l.Load(tx, 1)
		finishQueuedChunks(w)
		want := []ChunkPos{{1000, 0}}
		if !slices.Equal(g.positions, want) || !slices.Equal(v.chunks, want) {
			t.Fatalf("current centre: generated=%v delivered=%v", g.positions, v.chunks)
		}
	})
}

func TestLoaderRadiusAndCloseWithdrawQueuedWork(t *testing.T) {
	for _, closeLoader := range []bool{false, true} {
		name := "radius"
		if closeLoader {
			name = "close"
		}
		t.Run(name, func(t *testing.T) {
			withQueuedChunkWorld(t, func(tx *Tx) {
				w := tx.World()
				g := &recordingChunkGenerator{}
				w.conf.Generator = g
				l := NewLoader(1, w, NopViewer{})
				l.Load(tx, 9)
				if closeLoader {
					l.Close(tx)
				} else {
					l.ChangeRadius(tx, 0)
					defer l.Close(tx)
				}
				finishQueuedChunks(w)
				var want []ChunkPos
				if !closeLoader {
					want = []ChunkPos{{}}
				}
				if !slices.Equal(g.positions, want) || l.queued.Load() != 0 {
					t.Fatalf("generated=%v queued=%d", g.positions, l.queued.Load())
				}
			})
		})
	}
}

func TestLoaderPickupMakesRoomForAnotherRequest(t *testing.T) {
	withQueuedChunkWorld(t, func(tx *Tx) {
		w := tx.World()
		l := NewLoader(3, w, NopViewer{})
		defer l.Close(tx)
		l.Load(tx, 1)
		l.Load(tx, 1)
		l.Load(tx, 1)
		if len(l.pending) != 2 {
			t.Fatal("two-batch allowance was not enforced")
		}
		req := <-w.chunkWorkers.queue
		w.conf.Synchronous = true
		req.load(w)
		w.conf.Synchronous = false
		l.Load(tx, 1)
		if len(l.loaded) != 1 || len(l.pending) != 2 || l.queued.Load() != 2 {
			t.Fatal("worker pickup did not make room for the next request")
		}
	})
}

func TestLoaderReentryPreservesCurrentRequest(t *testing.T) {
	for _, skipped := range []bool{false, true} {
		name := "queued"
		if skipped {
			name = "skipped"
		}
		t.Run(name, func(t *testing.T) {
			withQueuedChunkWorld(t, func(tx *Tx) {
				w := tx.World()
				g := &recordingChunkGenerator{}
				w.conf.Generator = g
				v := &chunkQueueViewer{}
				l := NewLoader(3, w, v)
				defer l.Close(tx)
				l.Load(tx, 1)
				pos := ChunkPos{}
				old := w.chunkRequests[pos]
				l.Move(tx, mgl64.Vec3{16000, 0, 0})
				if skipped {
					if <-w.chunkWorkers.queue != old {
						t.Fatal("unexpected queued request")
					}
					if owned, needed := old.begin(); !owned || needed {
						t.Fatal("unwanted request was not skipped")
					}
					close(old.done)
				}
				l.Move(tx, mgl64.Vec3{})
				l.Load(tx, 1)
				current := w.chunkRequests[pos]
				if skipped {
					if current == old {
						t.Fatal("late caller attached to a skipped request")
					}
					old.signal(tx)
					if w.chunkRequests[pos] != current {
						t.Fatal("old completion erased its replacement")
					}
				} else if current != old {
					t.Fatal("live queued request was not shared")
				}
				finishQueuedChunks(w)
				want := []ChunkPos{pos}
				if !slices.Equal(g.positions, want) || !slices.Equal(v.chunks, want) || len(l.pending) != 0 || l.queued.Load() != 0 {
					t.Fatalf("reentry generated=%v delivered=%v pending=%d queued=%d", g.positions, v.chunks, len(l.pending), l.queued.Load())
				}
			})
		})
	}
}

func TestImmediateChunkClaimsQueuedWorkBeforeBusyWorkers(t *testing.T) {
	g := &ownerTakeChunkGenerator{started: make(chan ChunkPos, 2), release: make(chan struct{})}
	unblock := sync.OnceFunc(func() { close(g.release) })
	// Two workers allow a concurrency-safe generator; the one-worker configuration
	// intentionally serialises generator calls, including synchronous reads.
	w := Config{Generator: g, ChunkLoadWorkers: 2}.New()
	t.Cleanup(func() {
		unblock()
		w.Close()
		if g.targetCalls.Load() != 1 {
			t.Errorf("target generated %d times", g.targetCalls.Load())
		}
	})
	var l *Loader
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.Do(func(tx *Tx) {
		l = NewLoader(3, w, NopViewer{})
		g.target = l.loadQueue[7]
		l.Load(tx, 8)
	}).Wait(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case <-g.started:
		case <-ctx.Done():
			t.Fatal("workers did not start their blocked loads")
		}
	}
	read := w.Do(func(tx *Tx) {
		col := tx.chunk(g.target)
		if col == nil {
			t.Error("owner did not obtain the queued chunk")
		}
		l.Close(tx)
	})
	if err := read.Wait(ctx); err != nil {
		t.Fatal("synchronous read waited behind unrelated queued loads:", err)
	}
	// The owner-taken request is still in the channel. Abort must not close it twice.
	w.chunkWorkers.drainAndAbort()
	if l.queued.Load() != 0 {
		t.Fatalf("queue allowance after drain: %d", l.queued.Load())
	}
}

func TestLoaderWorldChangeSeparatesWaitingAllowances(t *testing.T) {
	withQueuedChunkWorld(t, func(tx *Tx) {
		w := tx.World()
		other := Config{Synchronous: true}.New()
		defer other.Close()
		l := NewLoader(1, w, NopViewer{})
		l.Load(tx, 1)
		old := l.queued
		l.ChangeWorld(tx, other)
		if l.queued == old || l.queued.Load() != 0 || len(l.pending) != 0 {
			t.Fatal("new world inherited an old waiting allowance")
		}
		finishQueuedChunks(w)
		if old.Load() != 0 || l.queued.Load() != 0 {
			t.Fatal("old completion changed the new world's allowance")
		}
		other.Do(func(tx *Tx) { l.Close(tx) })
	})
}

// finishQueuedChunks completes the fixture's queued requests without background workers.
func finishQueuedChunks(w *World) {
	w.conf.Synchronous = true
	defer func() { w.conf.Synchronous = false }()
	for {
		select {
		case req := <-w.chunkWorkers.queue:
			req.load(w)
		default:
			return
		}
	}
}

type recordingChunkGenerator struct {
	NopGenerator
	positions []ChunkPos
}

// GenerateChunk records requests executed by a fixture's single owner.
func (g *recordingChunkGenerator) GenerateChunk(pos ChunkPos, _ *chunk.Chunk) {
	g.positions = append(g.positions, pos)
}

type ownerTakeChunkGenerator struct {
	NopGenerator
	target      ChunkPos
	targetCalls atomic.Int32
	started     chan ChunkPos
	release     chan struct{}
}

// GenerateChunk holds unrelated loads while allowing the requested owner load to finish.
func (g *ownerTakeChunkGenerator) GenerateChunk(pos ChunkPos, _ *chunk.Chunk) {
	if pos == g.target {
		g.targetCalls.Add(1)
		return
	}
	select {
	case g.started <- pos:
	default:
	}
	<-g.release
}
