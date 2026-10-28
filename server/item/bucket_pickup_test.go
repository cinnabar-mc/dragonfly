package item_test

import (
	"testing"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

type bucketTerrain struct {
	blocks  map[cube.Pos]world.Block
	liquids map[cube.Pos]world.Liquid
	missing cube.Pos
}

// Block supplies the primary terrain layer.
func (s bucketTerrain) Block(pos cube.Pos) world.Block {
	if b, ok := s.blocks[pos]; ok {
		return b
	}
	return block.Air{}
}

// Liquid supplies a source from either terrain layer.
func (s bucketTerrain) Liquid(pos cube.Pos) (world.Liquid, bool) {
	liquid, ok := s.liquids[pos]
	return liquid, ok
}

// BlockAvailable rejects one missing terrain band probe.
func (s bucketTerrain) BlockAvailable(pos cube.Pos) bool { return pos != s.missing }

func TestBucket_PickupTarget(t *testing.T) {
	pos := cube.Pos{0, 64, 3}
	for _, name := range []string{"source", "flowing", "falling", "wall", "waterlogged solid", "unloaded source", "unloaded ray", "out of reach", "filled bucket", "intervening source", "submerged", "basin rim"} {
		t.Run(name, func(t *testing.T) {
			src := bucketTerrain{blocks: make(map[cube.Pos]world.Block), liquids: map[cube.Pos]world.Liquid{pos: block.Water{Depth: 8}}, missing: cube.Pos{99, 99, 99}}
			reach := 4.5
			eye := mgl64.Vec3{.5, 65.6, .5}
			bucket := item.Bucket{}
			switch name {
			case "basin rim":
				src.blocks[cube.Pos{0, 64, 2}] = block.Stone{}
			case "submerged":
				eye = pos.Vec3Centre()
			case "flowing":
				src.liquids[pos] = block.Water{Depth: 7}
			case "falling":
				src.liquids[pos] = block.Water{Depth: 8, Falling: true}
			case "wall":
				src.blocks[cube.Pos{0, 65, 1}] = block.Stone{}
			case "waterlogged solid":
				src.blocks[pos] = block.Stone{}
			case "unloaded source":
				src.missing = pos
			case "unloaded ray":
				src.missing = cube.Pos{0, 65, 1}
			case "out of reach":
				reach = 2
			case "filled bucket":
				bucket.Content = item.LiquidBucketContent(block.Water{Depth: 8})
			case "intervening source":
				src.liquids[cube.Pos{0, 65, 1}] = block.Water{Depth: 8}
			}
			hit, ok := bucket.PickupTarget(pos, src, cube.Range{-64, 319}, eye, reach)
			if ok != (name == "source" || name == "submerged" || name == "basin rim") {
				t.Fatalf("reachable = %v, hit = %v", ok, hit)
			}
			if ok && name == "basin rim" && hit.Face() != cube.FaceUp {
				t.Fatalf("basin did not select top face: %v", hit)
			}
			if ok && name != "submerged" && name != "basin rim" && (hit.Face() != cube.FaceNorth || hit.Position().Z() != 3) {
				t.Fatalf("wrong face/hit: %v", hit)
			}
		})
	}
}
