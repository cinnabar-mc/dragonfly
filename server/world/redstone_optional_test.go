package world

import (
	"testing"

	"github.com/df-mc/dragonfly/server/block/cube"
)

// optionalSource emits full power and acts on input, but only takes part in redstone when on.
type optionalSource struct{ on bool }

func (optionalSource) EncodeBlock() (string, map[string]any) { return "test:optional_source", nil }
func (optionalSource) Hash() (uint64, uint64)                { return 0, 0 }
func (optionalSource) Model() BlockModel                     { return nil }
func (s optionalSource) RedstoneComponent() bool             { return s.on }
func (optionalSource) RedstonePower(cube.Pos, *Tx, cube.Face) int {
	return 15
}
func (optionalSource) RedstonePowerAction(cube.Pos, *Tx, int, int) {}

// A block that opts out of redstone is classified as no component, whatever interfaces its type
// implements; one that opts in, or does not implement RedstoneOptional, keeps them.
func TestRedstoneOptionalBlocksOptOut(t *testing.T) {
	if isRedstoneRelevant(optionalSource{on: false}) || RedstoneComponent(optionalSource{on: false}) {
		t.Fatal("an opted-out block is still a redstone component")
	}
	source, _, action, _ := classifyRedstoneBlock(optionalSource{on: true})
	if !source || !action || !RedstoneComponent(optionalSource{on: true}) {
		t.Fatal("an opted-in block lost its redstone interfaces")
	}
	if !RedstoneComponent(nil) {
		t.Fatal("a block without RedstoneOptional is not a redstone component")
	}
}
