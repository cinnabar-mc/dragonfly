package world

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/df-mc/dragonfly/server/world/chunk"
)

// blockedUnloadProvider holds the first save until the test releases it.
type blockedUnloadProvider struct {
	NopProvider
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	saved   []uint32
	entity  []byte
	fail    error
}

// StoreColumn records terrain after waiting, so a revived column must not share it.
func (p *blockedUnloadProvider) StoreColumn(_ ChunkPos, _ Dimension, col *chunk.Column) error {
	first := false
	p.once.Do(func() {
		first = true
		close(p.entered)
		<-p.release
	})
	p.mu.Lock()
	p.saved = append(p.saved, col.Chunk.Biome(0, -64, 0))
	if len(col.Entities) != 0 {
		p.entity = append(p.entity, col.Entities[0].Data["payload"].([]byte)[0])
	}
	p.mu.Unlock()
	if first {
		return p.fail
	}
	return nil
}

func TestUnusedChunkSaveKeepsWorldResponsiveAndReloadsCurrentTerrain(t *testing.T) {
	for _, useLoader := range []bool{false, true} {
		name := "immediate"
		if useLoader {
			name = "loader"
		}
		t.Run(name, func(t *testing.T) { testUnusedChunkReload(t, useLoader) })
	}
}

// testUnusedChunkReload checks both owner read paths while provider storage is blocked.
func testUnusedChunkReload(t *testing.T, useLoader bool) {
	t.Helper()
	provider := &blockedUnloadProvider{entered: make(chan struct{}), release: make(chan struct{})}
	w := Config{Provider: provider, ChunkUnloadInterval: time.Millisecond, SaveInterval: -1, RandomTickSpeed: -1}.New()
	var release sync.Once
	unblock := func() { release.Do(func() { close(provider.release) }) }
	defer func() { unblock(); w.Close() }()
	pos := ChunkPos{3, 4}
	<-w.exec(func(tx *Tx) {
		c := tx.chunk(pos)
		c.SetBiome(0, -64, 0, 7)
		c.modified = true
		c.unsupportedEntities = []chunk.Entity{{ID: 17, Data: map[string]any{
			"identifier": "test:unknown", "payload": []byte{7},
		}}}
	})
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("unused column was not offered for saving")
	}

	// The provider remains blocked throughout this owner transaction. A reload
	// must use the pending terrain, without a stale provider read or regeneration.
	task := w.Do(func(tx *Tx) {
		var c *Column
		if useLoader {
			if !w.loadChunkAsync(tx, pos, chunkCallback{receive: func(_ *Tx, col *Column) { c = col }}, nil, 1) || c == nil {
				t.Error("loader did not receive pending terrain immediately")
				return
			}
		} else {
			c = tx.chunk(pos)
		}
		if got := c.Biome(0, -64, 0); got != 7 {
			t.Errorf("reloaded biome = %d, want pending biome 7", got)
			return
		}
		c.SetBiome(0, -64, 0, 23)
		c.unsupportedEntities[0].Data["payload"].([]byte)[0] = 23
		c.viewers = []Viewer{NopViewer{}}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := task.Wait(ctx); err != nil {
		t.Fatalf("world stopped while unused terrain was being saved: %v", err)
	}
	unblock()
	w.Close()
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.saved) != 2 || provider.saved[0] != 7 || provider.saved[1] != 23 {
		t.Fatalf("saved terrain = %v, want immutable snapshot 7 followed by current terrain 23", provider.saved)
	}
	if len(provider.entity) != 2 || provider.entity[0] != 7 || provider.entity[1] != 23 {
		t.Fatalf("saved entity NBT = %v, want detached snapshot 7 followed by current NBT 23", provider.entity)
	}
}

func TestUnusedChunkSaveShutdownRetriesFailedTerrain(t *testing.T) {
	provider := &blockedUnloadProvider{
		entered: make(chan struct{}), release: make(chan struct{}), fail: errors.New("controlled storage failure"),
	}
	w := Config{Provider: provider, ChunkUnloadInterval: time.Millisecond, SaveInterval: -1, RandomTickSpeed: -1}.New()
	var release sync.Once
	unblock := func() { release.Do(func() { close(provider.release) }) }
	defer func() { unblock(); w.Close() }()
	<-w.exec(func(tx *Tx) {
		c := tx.chunk(ChunkPos{3, 4})
		c.SetBiome(0, -64, 0, 7)
		c.modified = true
	})
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("unused column was not offered for saving")
	}
	closed := make(chan struct{})
	go func() { w.Close(); close(closed) }()
	<-w.closeStarted
	select {
	case <-closed:
		t.Fatal("shutdown completed with a provider write still in progress")
	default:
	}
	unblock()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not drain the failed save and its retry")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.saved) != 2 || provider.saved[0] != 7 || provider.saved[1] != 7 {
		t.Fatalf("shutdown save attempts = %v, want failed snapshot 7 followed by retry 7", provider.saved)
	}
}
