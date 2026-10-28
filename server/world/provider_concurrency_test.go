package world

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/df-mc/dragonfly/server/world/chunk"
)

// coldReadProvider supports independent reads while its write is deliberately stalled.
type coldReadProvider struct {
	NopProvider
	storing    chan struct{}
	release    chan struct{}
	reads      chan ChunkPos
	once       sync.Once
	concurrent bool
}

// ConcurrentColumnReads opts this thread-safe fixture into independent column reads.
func (p *coldReadProvider) ConcurrentColumnReads() bool { return p.concurrent }

// LoadColumn records cold reads before returning an ordinary generation miss.
func (p *coldReadProvider) LoadColumn(pos ChunkPos, dim Dimension) (*chunk.Column, error) {
	if pos[0] >= 100 {
		p.reads <- pos
	}
	return p.NopProvider.LoadColumn(pos, dim)
}

// StoreColumn holds the provider write until the test releases it.
func (p *coldReadProvider) StoreColumn(ChunkPos, Dimension, *chunk.Column) error {
	p.once.Do(func() { close(p.storing); <-p.release })
	return nil
}

func TestColdColumnReadsRespectProviderConcurrency(t *testing.T) {
	for _, mode := range []string{"legacy", "disabled", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			concurrent := mode == "concurrent"
			p := &coldReadProvider{storing: make(chan struct{}), release: make(chan struct{}), reads: make(chan ChunkPos, 2), concurrent: concurrent}
			var provider Provider = p
			if mode == "legacy" {
				// Expose only Provider, hiding the optional concurrency method.
				provider = struct{ Provider }{p}
			}
			w := Config{Provider: provider, ChunkUnloadInterval: time.Millisecond, SaveInterval: -1, RandomTickSpeed: -1}.New()
			var release sync.Once
			unblock := func() { release.Do(func() { close(p.release) }) }
			defer func() { unblock(); w.Close() }()
			<-w.exec(func(tx *Tx) { tx.chunk(ChunkPos{}).modified = true })
			select {
			case <-p.storing:
			case <-time.After(5 * time.Second):
				t.Fatal("unused column did not reach storage")
			}
			loaded := make(chan struct{})
			started := make(chan struct{})
			task := w.Do(func(tx *Tx) {
				if !w.loadChunkAsync(tx, ChunkPos{100, 0}, chunkCallback{receive: func(*Tx, *Column) { close(loaded) }}, nil, 1) {
					t.Error("cold worker load was rejected")
				}
				close(started)
				tx.chunk(ChunkPos{101, 0})
			})
			<-started
			if concurrent {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := task.Wait(ctx); err != nil {
					t.Fatalf("cold owner read waited behind an unrelated store: %v", err)
				}
				select {
				case <-loaded:
				case <-time.After(time.Second):
					t.Fatal("cold worker read waited behind an unrelated store")
				}
			} else {
				select {
				case <-p.reads:
					t.Fatal("provider without a concurrency capability was read during a store")
				case <-time.After(100 * time.Millisecond):
				}
			}
			unblock()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := task.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-loaded:
			case <-time.After(5 * time.Second):
				t.Fatal("worker read did not finish after storage was released")
			}
		})
	}
}
