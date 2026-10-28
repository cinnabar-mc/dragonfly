package mcdb

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/chunk"
	"github.com/df-mc/goleveldb/leveldb/opt"
)

// advancingColumnReader replaces a column after its biome record has been read.
type advancingColumnReader struct {
	columnReader
	key     []byte
	advance func()
	once    sync.Once
}

// columnReadResult transfers a completed read back to the test goroutine.
type columnReadResult struct {
	column *chunk.Column
	err    error
}

// Get forces a committed update between records in one column load.
func (r *advancingColumnReader) Get(key []byte, options *opt.ReadOptions) ([]byte, error) {
	data, err := r.columnReader.Get(key, options)
	if bytes.Equal(key, r.key) {
		r.once.Do(r.advance)
	}
	return data, err
}

// snapshotColumn marks terrain, actors, block entities, and ticks with the same version.
func snapshotColumn(version int32) *chunk.Column {
	c := chunk.New(world.DefaultBlockRegistry, world.Overworld.Range())
	c.SetBiome(0, -64, 0, uint32(version))
	if version == 7 {
		c.MarkLegacyStates()
	}
	return &chunk.Column{
		Chunk: c, Tick: int64(version),
		Entities:      []chunk.Entity{{ID: 17, Data: map[string]any{"identifier": "test:actor", "version": version}}},
		BlockEntities: []chunk.BlockEntity{{Pos: cube.Pos{0, -64, 0}, Data: map[string]any{"version": version}}},
		ScheduledBlocks: []chunk.ScheduledBlockUpdate{{
			Pos: cube.Pos{0, -64, 0}, Block: world.DefaultBlockRegistry.AirRuntimeID(), Tick: int64(version) + 5,
		}},
	}
}

// checkColumnVersion checks the independently stored records that must agree in a load.
func checkColumnVersion(t *testing.T, col *chunk.Column, biome, other int32) {
	t.Helper()
	if col.Chunk.Biome(0, -64, 0) != uint32(biome) || col.Tick != int64(other) || col.Chunk.LegacyStates() != (other == 7) {
		t.Fatalf("column has biome=%d tick=%d legacy=%t; want biome=%d metadata=%d", col.Chunk.Biome(0, -64, 0), col.Tick, col.Chunk.LegacyStates(), biome, other)
	}
	if len(col.Entities) != 1 || col.Entities[0].Data["version"] != other ||
		len(col.BlockEntities) != 1 || col.BlockEntities[0].Data["version"] != other ||
		len(col.ScheduledBlocks) != 1 || col.ScheduledBlocks[0].Tick != int64(other)+5 {
		t.Fatalf("actor, block entity, or scheduled updates do not match version %d", other)
	}
}

func TestColumnSnapshotKeepsRecordsConsistentDuringWrite(t *testing.T) {
	for _, frozen := range []bool{false, true} {
		name := "live_read_control"
		if frozen {
			name = "snapshot"
		}
		t.Run(name, func(t *testing.T) {
			db, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			k := dbKey{pos: world.ChunkPos{3, 4}, dim: world.Overworld}
			if err := db.StoreColumn(k.pos, k.dim, snapshotColumn(7)); err != nil {
				t.Fatal(err)
			}
			var reader columnReader = db.ldb
			if frozen {
				snapshot, err := db.ldb.GetSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				defer snapshot.Release()
				reader = snapshot
			}
			advancing := &advancingColumnReader{columnReader: reader, key: k.Sum(key3DData), advance: func() {
				if err := db.StoreColumn(k.pos, k.dim, snapshotColumn(23)); err != nil {
					t.Fatal(err)
				}
			}}
			col, err := db.column(k, advancing)
			if err != nil {
				t.Fatal(err)
			}
			other := int32(23)
			if frozen {
				other = 7
			}
			checkColumnVersion(t, col, 7, other)

			// The known provider's cold read also finishes while its writer
			// serialization is held. It must not inherit that write-side wait.
			db.storeMu.Lock()
			loaded := make(chan columnReadResult, 1)
			go func() {
				current, err := db.LoadColumn(k.pos, k.dim)
				loaded <- columnReadResult{column: current, err: err}
			}()
			select {
			case result := <-loaded:
				db.storeMu.Unlock()
				if result.err != nil {
					t.Fatal(result.err)
				}
				checkColumnVersion(t, result.column, 23, 23)
			case <-time.After(time.Second):
				db.storeMu.Unlock()
				<-loaded
				t.Fatal("column read waited for writer serialization")
			}
		})
	}
}
