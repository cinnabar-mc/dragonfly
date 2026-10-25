package player

import (
	"testing"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

// A block item used on an Activatable block activates it and is not placed, while
// UseItemOnBlockWithoutActivation places it against the block, leaves the block alone and takes
// the placed item from the hand.
func TestUseItemOnBlockWithoutActivationPlacesAgainstActivatable(t *testing.T) {
	w := world.Config{Synchronous: true}.New()
	defer w.Close()
	leverPos := cube.Pos{10, 70, 8}
	placedPos := leverPos.Side(cube.FaceEast)
	spawn := world.EntitySpawnOpts{}.New(Type, Config{Position: mgl64.Vec3{8, 70, 8}, GameMode: world.GameModeSurvival})
	w.Do(func(tx *world.Tx) {
		tx.SetBlock(leverPos.Side(cube.FaceDown), block.Stone{}, nil)
		tx.SetBlock(leverPos, block.Lever{Facing: cube.FaceUp}, nil)
		p := tx.AddEntity(spawn).(*Player)
		p.SetHeldItems(item.NewStack(block.Stone{}, 2), item.Stack{})

		p.UseItemOnBlock(leverPos, cube.FaceEast, mgl64.Vec3{1, 0.5, 0.5})
		if lever := tx.Block(leverPos).(block.Lever); !lever.Powered {
			t.Fatal("UseItemOnBlock did not activate the lever")
		}
		if _, ok := tx.Block(placedPos).(block.Air); !ok {
			t.Fatalf("UseItemOnBlock placed %T beside an activated block", tx.Block(placedPos))
		}

		p.UseItemOnBlockWithoutActivation(leverPos, cube.FaceEast, mgl64.Vec3{1, 0.5, 0.5})
		if lever := tx.Block(leverPos).(block.Lever); !lever.Powered {
			t.Fatal("UseItemOnBlockWithoutActivation activated the lever")
		}
		if _, ok := tx.Block(placedPos).(block.Stone); !ok {
			t.Fatalf("UseItemOnBlockWithoutActivation left %T, want stone placed", tx.Block(placedPos))
		}
		if held, _ := p.HeldItems(); held.Count() != 1 {
			t.Fatalf("held %d stone after placing one of 2", held.Count())
		}
	})
}
