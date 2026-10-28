package mcdb

import (
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/chunk"
	"testing"
)

func TestMovedActorSurvivesSourceColumnSave(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source, destination := world.ChunkPos{}, world.ChunkPos{1, 0}
	empty := func() *chunk.Column {
		return &chunk.Column{Chunk: chunk.New(world.DefaultBlockRegistry, world.Overworld.Range())}
	}
	actor := chunk.Entity{ID: 42, Data: map[string]any{"identifier": "minecraft:item"}}
	original := empty()
	original.Entities = []chunk.Entity{actor}
	if err := db.StoreColumn(source, world.Overworld, original); err != nil {
		t.Fatal(err)
	}
	moved := empty()
	moved.Entities = []chunk.Entity{actor}
	// A destination may be saved before its dirty source column.
	if err := db.StoreColumn(destination, world.Overworld, moved); err != nil {
		t.Fatal(err)
	}
	if err := db.StoreColumn(source, world.Overworld, empty()); err != nil {
		t.Fatal(err)
	}
	got, err := db.entities(dbKey{pos: destination, dim: world.Overworld}, db.ldb)
	if err != nil {
		t.Fatalf("destination actor was deleted by source save: %v", err)
	}
	if len(got) != 1 || got[0].ID != actor.ID {
		t.Fatalf("destination actors = %v", got)
	}
	if err := db.StoreColumn(destination, world.Overworld, empty()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ldb.Get(entityIndex(actor.ID), nil); err == nil {
		t.Fatal("removed actor data survived final owner removal")
	}
}
