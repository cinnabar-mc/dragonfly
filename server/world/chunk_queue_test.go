package world

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world/chunk"
	"github.com/go-gl/mathgl/mgl64"
)

func TestLoaderAlignedBurstsKeepEachLoadersWorkAvailable(t *testing.T) {
	withQueuedChunkWorld(t, func(tx *Tx) {
		w := tx.World()
		w.conf.ChunkLoadWorkers = 2
		for i := range 6 {
			l := NewLoader(5, w, NopViewer{})
			defer l.Close(tx)
			l.Move(tx, mgl64.Vec3{float64(i * 1600), 0, 0})
			l.Load(tx, 4)
			if got := len(l.pending); got != 4 {
				t.Fatalf("loader %d offered four chunks but only %d remain available to the workers", i, got)
			}
			// Another burst stays available while the previous one is still waiting.
			l.Load(tx, 4)
			if got := len(l.pending); got != 8 {
				t.Fatalf("loader %d could not queue a second burst: %d", i, got)
			}
			l.Load(tx, 4)
			if got := len(l.pending); got != 8 {
				t.Fatalf("loader %d admitted a third waiting batch: %d", i, got)
			}
		}
	})
}

func TestChunkWorkerQueueRetainsTotalCapacity(t *testing.T) {
	w := &World{conf: Config{ChunkLoadWorkers: 2}}
	pool := newChunkWorkerPool(w)
	for i := range cap(pool.queue) {
		if !pool.schedule(&chunkRequest{done: make(chan struct{})}, 1) {
			t.Fatalf("only %d of %d waiting slots accepted work", i, cap(pool.queue))
		}
	}
	if pool.schedule(&chunkRequest{done: make(chan struct{})}, 1) {
		t.Fatal("exceeded total queue capacity")
	}
	pool.drainAndAbort()
}

func TestLoaderBackpressureKeepsNearestRequestFirst(t *testing.T) {
	for _, budget := range []int{1, 4} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			withQueuedChunkWorld(t, func(tx *Tx) {
				w := tx.World()
				l := NewLoader(3, w, NopViewer{})
				fillChunkQueue(t, w.chunkWorkers)
				defer l.Close(tx)
				before := slices.Clone(l.loadQueue)
				// A ready chunk behind the rejected request must wait for the next Load call.
				w.chunks[before[1]] = &Column{Chunk: chunk.New(w.conf.Blocks, w.Range())}

				l.Load(tx, budget)

				if !slices.Equal(l.loadQueue, before) {
					t.Fatalf("backpressure reordered the load queue: got %v, want %v", l.loadQueue, before)
				}
				if len(l.pending) != 0 || len(l.loaded) != 0 || len(w.chunkRequests) != 0 {
					t.Fatalf("rejected load changed request state: pending=%d loaded=%d requests=%d", len(l.pending), len(l.loaded), len(w.chunkRequests))
				}
			})
		})
	}
}

func TestLoaderMoveReprioritisesDeferredWork(t *testing.T) {
	withQueuedChunkWorld(t, func(tx *Tx) {
		w := tx.World()
		l := NewLoader(3, w, NopViewer{})
		fillChunkQueue(t, w.chunkWorkers)
		defer l.Close(tx)
		l.Load(tx, 48)
		l.Move(tx, mgl64.Vec3{1600, 0, 0})
		centre := ChunkPos{100, 0}
		if l.loadQueue[0] != centre {
			t.Fatalf("nearest deferred request = %v, want %v", l.loadQueue[0], centre)
		}

		// Free one waiting slot without completing any other loader's request.
		req := <-w.chunkWorkers.queue
		req.abort()
		l.Load(tx, 48)
		if _, ok := l.pending[centre]; !ok {
			t.Fatal("current centre was not admitted when a slot became available")
		}
		if len(l.pending) != 1 || len(w.chunkRequests) != 1 {
			t.Fatal("deferred work was lost or admitted more than once")
		}
	})
}

func TestChunkQueueSaturationPreservesSharedAndImmediateLoads(t *testing.T) {
	withQueuedChunkWorld(t, func(tx *Tx) {
		w := tx.World()
		pos := ChunkPos{}
		first, second := &chunkQueueViewer{}, &chunkQueueViewer{}
		l1, l2 := NewLoader(1, w, first), NewLoader(1, w, second)
		defer l1.Close(tx)
		defer l2.Close(tx)
		l1.Load(tx, 1)
		req := w.chunkRequests[pos]
		fillChunkQueue(t, w.chunkWorkers)
		l2.Load(tx, 1)
		if w.chunkRequests[pos] != req || len(req.callbacks) != 2 {
			t.Fatal("saturation prevented two loaders from sharing one request")
		}
		l1.Move(tx, mgl64.Vec3{1600, 0, 0})

		if got := <-w.chunkWorkers.queue; got != req {
			t.Fatal("accepted request order changed")
		}
		if owned, needed := req.begin(); !owned || !needed {
			t.Fatal("shared request was not claimed for loading")
		}
		var err error
		req.col, err = w.loadChunk(pos)
		if err != nil {
			t.Fatal(err)
		}
		close(req.done)
		col := req.doImmediate(tx)
		req.signal(tx)
		if col == nil || len(w.chunkRequests) != 0 || len(l1.pending) != 0 || len(l2.pending) != 0 {
			t.Fatal("immediate completion did not retire the shared request")
		}
		if len(first.chunks) != 0 || !slices.Equal(second.chunks, []ChunkPos{pos}) {
			t.Fatalf("shared completion delivery: moved=%v current=%v", first.chunks, second.chunks)
		}

		fillChunkQueue(t, w.chunkWorkers)
		calls := 0
		if !w.loadChunkAsync(tx, pos, chunkCallback{receive: func(gotTx *Tx, got *Column) {
			if gotTx != tx || got != col {
				t.Error("resident load changed its owner or column")
			}
			calls++
		}}, nil, 1) || calls != 1 {
			t.Fatal("a full queue blocked an already resident chunk")
		}
	})
}

func TestChunkWorkerQueueCloseReleasesWaitingRequests(t *testing.T) {
	w := &World{conf: Config{ChunkLoadWorkers: 1}}
	pool := newChunkWorkerPool(w)
	requests := make([]*chunkRequest, 2)
	for i := range requests {
		requests[i] = &chunkRequest{done: make(chan struct{})}
		if !pool.schedule(requests[i], 1) {
			t.Fatal("request was not accepted before close")
		}
	}
	w.closed.Store(true)
	if pool.schedule(&chunkRequest{done: make(chan struct{})}, 1) {
		t.Fatal("closed world accepted another request")
	}
	pool.drainAndAbort()
	pool.drainAndAbort()
	for _, req := range requests {
		select {
		case <-req.done:
		default:
			t.Fatal("closing left a queued request waiting")
		}
	}
}

func TestChunkWorkerBackpressureAllowsImmediateCompletion(t *testing.T) {
	started, release := make(chan ChunkPos, 4), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	w := Config{Generator: queuedChunkGenerator{started: started, release: release}}.New()
	t.Cleanup(func() {
		unblock()
		w.Close()
	})
	var l *Loader
	viewer := &chunkQueueViewer{}
	w.Do(func(tx *Tx) {
		l = NewLoader(3, w, viewer)
		l.Load(tx, 1)
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start the first request")
	}
	entered := make(chan struct{})
	task := w.Do(func(tx *Tx) {
		l.Load(tx, 4)
		if got := len(w.chunkWorkers.queue); got != 4 {
			t.Errorf("waiting requests = %d, want 4 for the caller's batch", got)
		}
		close(entered)
		col, ok := w.chunkFromAsyncPool(tx, ChunkPos{})
		if !ok || col == nil || l.loaded[ChunkPos{}] != col {
			t.Error("immediate load did not install and deliver the accepted request")
		}
		count := 0
		for _, pos := range viewer.chunks {
			if pos == (ChunkPos{}) {
				count++
			}
		}
		if count != 1 {
			t.Errorf("centre deliveries = %d, want 1", count)
		}
		l.Close(tx)
	})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("owner did not reach immediate load")
	}
	select {
	case <-task.Done():
		t.Error("immediate load completed while its generator was blocked")
	default:
	}
	unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := task.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkLoaderBackpressure(b *testing.B) {
	withQueuedChunkWorld(b, func(tx *Tx) {
		w := tx.World()
		l := NewLoader(20, w, NopViewer{})
		fillChunkQueue(b, w.chunkWorkers)
		defer l.Close(tx)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			l.Load(tx, 48)
		}
		b.StopTimer()
	})
}

// withQueuedChunkWorld drives scheduling on an owner without starting workers.
func withQueuedChunkWorld(t testing.TB, f func(*Tx)) {
	t.Helper()
	w := Config{Synchronous: true, ChunkLoadWorkers: 1}.New()
	defer w.Close()
	task := w.Do(func(tx *Tx) {
		w.conf.Synchronous = false
		defer func() {
			w.conf.Synchronous = true
			w.chunkWorkers.drainAndAbort()
		}()
		f(tx)
	})
	if err := task.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// fillChunkQueue fills the admission allowance, leaving accepted requests in order.
func fillChunkQueue(t testing.TB, pool *chunkWorkerPool) {
	t.Helper()
	for pool.schedule(&chunkRequest{done: make(chan struct{})}, 1) {
	}
	if len(pool.queue) == 0 {
		t.Fatal("queue did not accept any fixture requests")
	}
}

type chunkQueueViewer struct {
	NopViewer
	chunks []ChunkPos
}

// ViewChunk records deliveries so moved and current loaders can be compared.
func (v *chunkQueueViewer) ViewChunk(pos ChunkPos, _ Dimension, _ map[cube.Pos]Block, _ *chunk.Chunk) {
	v.chunks = append(v.chunks, pos)
}

type queuedChunkGenerator struct {
	NopGenerator
	started chan<- ChunkPos
	release <-chan struct{}
}

// GenerateChunk holds accepted work until the test releases the worker.
func (g queuedChunkGenerator) GenerateChunk(pos ChunkPos, _ *chunk.Chunk) {
	select {
	case g.started <- pos:
	default:
	}
	<-g.release
}
