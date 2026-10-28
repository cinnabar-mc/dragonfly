package world

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/df-mc/dragonfly/server/world/chunk"
)

// orderedSaveProvider records actual persisted terrain rather than just save completion.
type orderedSaveProvider struct {
	NopProvider
	mu     sync.Mutex
	writes []uint32
}

// StoreColumn persists the submitted biome so the test can detect an older overwrite.
func (p *orderedSaveProvider) StoreColumn(_ ChunkPos, _ Dimension, col *chunk.Column) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, col.Chunk.Biome(0, -64, 0))
	return nil
}

// delayedFirstStore holds the old write before the provider's serialization lock.
type delayedFirstStore struct {
	Provider
	first   chan struct{}
	release chan struct{}
	newer   chan struct{}
	calls   atomic.Int32
	fail    error
}

// StoreColumn exposes the scheduling gap between detachment and provider lock acquisition.
func (p *delayedFirstStore) StoreColumn(pos ChunkPos, dim Dimension, col *chunk.Column) error {
	first := p.calls.Add(1) == 1
	if first {
		close(p.first)
		<-p.release
		if p.fail != nil {
			return p.fail
		}
	}
	err := p.Provider.StoreColumn(pos, dim, col)
	if !first {
		select {
		case p.newer <- struct{}{}:
		default:
		}
	}
	return err
}

func TestSavePersistsRevivedTerrainAfterDelayedOlderWrite(t *testing.T) {
	for _, tt := range []struct {
		name            string
		savers          int
		closing, failed bool
		want            []uint32
	}{
		{name: "single", savers: 1, want: []uint32{7, 23}},
		{name: "concurrent", savers: 2, want: []uint32{7, 23, 23}},
		{name: "failed_old", savers: 1, failed: true, want: []uint32{23}},
		{name: "close", savers: 2, closing: true, want: []uint32{7, 23}},
		{name: "failed_close", savers: 2, closing: true, failed: true, want: []uint32{23}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := &orderedSaveProvider{}
			w := Config{Provider: provider, ChunkUnloadInterval: time.Hour, SaveInterval: -1, RandomTickSpeed: -1}.New()
			gate := &delayedFirstStore{first: make(chan struct{}), release: make(chan struct{}), newer: make(chan struct{}, 1)}
			if tt.failed {
				gate.fail = errors.New("controlled old-write failure")
			}
			var release sync.Once
			unblock := func() { release.Do(func() { close(gate.release) }) }
			defer func() { unblock(); w.Close() }()
			pos := ChunkPos{3, 4}
			<-w.exec(func(tx *Tx) {
				gate.Provider = w.conf.Provider
				w.conf.Provider = gate
				c := tx.chunk(pos)
				c.SetBiome(0, -64, 0, 7)
				c.modified = true
			})
			unloaded := make(chan struct{})
			go func() { w.closeUnusedChunks(); close(unloaded) }()
			select {
			case <-gate.first:
			case <-time.After(5 * time.Second):
				t.Fatal("old terrain did not reach the pre-provider gate")
			}
			task := w.Do(func(tx *Tx) {
				c := tx.chunk(pos)
				c.SetBiome(0, -64, 0, 23)
				c.viewers = []Viewer{NopViewer{}}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := task.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			saved := make(chan struct{})
			var saving sync.WaitGroup
			saving.Add(tt.savers)
			for range tt.savers {
				go func() { defer saving.Done(); w.Save() }()
			}
			go func() { saving.Wait(); close(saved) }()
			closed := make(chan struct{})
			if tt.closing {
				go func() { w.Close(); close(closed) }()
				<-w.closeStarted
			}
			// The old implementation lets the newer write finish first. A correct
			// serialization owner keeps it waiting until the old write is released.
			select {
			case <-gate.newer:
			case <-time.After(time.Second):
			}
			unblock()
			select {
			case <-saved:
			case <-time.After(5 * time.Second):
				t.Fatal("Save did not finish after the old write was released")
			}
			<-unloaded
			if tt.closing {
				select {
				case <-closed:
				case <-time.After(5 * time.Second):
					t.Fatal("Close did not drain saves")
				}
			}
			provider.mu.Lock()
			defer provider.mu.Unlock()
			if got := provider.writes; !slices.Equal(got, tt.want) {
				t.Fatalf("persisted terrain history = %v, want %v", got, tt.want)
			}
		})
	}
}
