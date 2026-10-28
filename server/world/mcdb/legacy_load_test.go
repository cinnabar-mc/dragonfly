package mcdb

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/goleveldb/leveldb"
	"github.com/sandertv/gophertunnel/minecraft/nbt"
)

func TestConcurrentLegacyColumnLoads(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var sub bytes.Buffer
	sub.Write([]byte{8, 1, 0}) // One storage with a single pre-1.13 palette entry.
	legacyChest := map[string]any{"name": "minecraft:chest", "val": int16(2)}
	if err := nbt.NewEncoderWithEncoding(&sub, nbt.LittleEndian).Encode(legacyChest); err != nil {
		t.Fatal(err)
	}
	ticks, err := nbt.MarshalEncoding(scheduledUpdates{CurrentTick: 7, TickList: []map[string]any{{
		"x": int32(1), "y": int32(0), "z": int32(1), "time": int64(9), "blockState": legacyChest,
	}}}, nbt.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	positions := []world.ChunkPos{{3, 4}, {5, 6}}
	batch := new(leveldb.Batch)
	for _, pos := range positions {
		key := dbKey{pos: pos, dim: world.Overworld}
		batch.Put(key.Sum(keyVersion), []byte{chunkVersion})
		batch.Put(key.Sum(keySubChunkData, 0), sub.Bytes())
		batch.Put(key.Sum(keyPendingScheduledTicks), ticks)
	}
	if err := db.ldb.Write(batch, nil); err != nil {
		t.Fatal(err)
	}
	want, ok := world.DefaultBlockRegistry.StateToRuntimeID("minecraft:chest", map[string]any{"minecraft:cardinal_direction": "north"})
	if !ok {
		t.Fatal("north-facing chest is not registered")
	}
	const readers = 8
	start := make(chan struct{})
	errors := make(chan error, readers)
	var loads sync.WaitGroup
	for i := range readers {
		loads.Go(func() {
			<-start
			for range 32 {
				col, err := db.LoadColumn(positions[i%len(positions)], world.Overworld)
				if err != nil {
					errors <- err
					return
				}
				if col.Chunk.Block(1, 0, 1, 0) != want || !col.Chunk.LegacyStates() ||
					len(col.ScheduledBlocks) != 1 || col.ScheduledBlocks[0].Block != want || col.ScheduledBlocks[0].Tick != 9 {
					errors <- fmt.Errorf("concurrent load changed legacy chest terrain or its scheduled tick")
					return
				}
			}
		})
	}
	close(start)
	loads.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}
