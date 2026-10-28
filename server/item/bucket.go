package item

import (
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/block/cube/trace"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/sound"
	"github.com/go-gl/mathgl/mgl64"
	"time"
)

// BucketContent is the content of a bucket.
type BucketContent struct {
	liquid world.Liquid
	milk   bool
}

// LiquidBucketContent returns a new BucketContent with the liquid passed in.
func LiquidBucketContent(l world.Liquid) BucketContent {
	return BucketContent{liquid: l}
}

// MilkBucketContent returns a new BucketContent with the milk flag set.
func MilkBucketContent() BucketContent {
	return BucketContent{milk: true}
}

// Liquid returns the world.Liquid that a Bucket with this BucketContent places.
// If this BucketContent does not place a liquid block, false is returned.
func (b BucketContent) Liquid() (world.Liquid, bool) {
	return b.liquid, b.liquid != nil
}

// String converts the BucketContent to a string.
func (b BucketContent) String() string {
	if b.milk {
		return "milk"
	} else if b.liquid != nil {
		return b.liquid.LiquidType()
	}
	return ""
}

// LiquidType returns the type of liquid the bucket contains.
func (b BucketContent) LiquidType() string {
	if b.liquid != nil {
		return b.liquid.LiquidType()
	}
	return "milk"
}

// Bucket is a tool used to carry water, lava and fish.
type Bucket struct {
	// Content is the content that the bucket has. By default, this value resolves to an empty bucket.
	Content BucketContent
}

// MaxCount returns 16.
func (b Bucket) MaxCount() int {
	if b.Empty() {
		return 16
	}
	return 1
}

// AlwaysConsumable ...
func (b Bucket) AlwaysConsumable() bool {
	return b.Content.milk
}

// CanConsume ...
func (b Bucket) CanConsume() bool {
	return b.Content.milk
}

// ConsumeDuration ...
func (b Bucket) ConsumeDuration() time.Duration {
	return DefaultConsumeDuration
}

// Consume ...
func (b Bucket) Consume(_ *world.Tx, c Consumer) Stack {
	for _, effect := range c.Effects() {
		c.RemoveEffect(effect.Type())
	}
	return NewStack(Bucket{}, 1)
}

// Empty returns true if the bucket is empty.
func (b Bucket) Empty() bool {
	return b.Content.liquid == nil && !b.Content.milk
}

// FuelInfo ...
func (b Bucket) FuelInfo() FuelInfo {
	if liq := b.Content.liquid; liq != nil && liq.LiquidType() == "lava" {
		return newFuelInfo(time.Second * 1000).WithResidue(NewStack(Bucket{}, 1))
	}
	return FuelInfo{}
}

// UseOnBlock handles the bucket filling and emptying logic.
func (b Bucket) UseOnBlock(pos cube.Pos, face cube.Face, _ mgl64.Vec3, tx *world.Tx, _ User, ctx *UseContext) bool {
	if b.Content.milk {
		return false
	}
	if b.Empty() {
		return b.fillFrom(pos, tx, ctx)
	}
	liq := b.Content.liquid.WithDepth(8, false)
	if bl := tx.Block(pos); canDisplace(bl, liq) || replaceableWith(bl, liq) {
		tx.SetLiquid(pos, liq)
	} else if bl := tx.Block(pos.Side(face)); canDisplace(bl, liq) || replaceableWith(bl, liq) {
		tx.SetLiquid(pos.Side(face), liq)
	} else {
		return false
	}

	tx.PlaySound(pos.Vec3Centre(), sound.BucketEmpty{Liquid: b.Content.liquid})
	ctx.NewItem = NewStack(Bucket{}, 1)
	ctx.NewItemSurvivalOnly = true
	ctx.SubtractFromCount(1)
	return true
}

// fillFrom fills a bucket from the liquid at the position passed in the world. If there is no liquid or if
// the liquid is no source, fillFrom returns false.
func (b Bucket) fillFrom(pos cube.Pos, tx *world.Tx, ctx *UseContext) bool {
	liquid, ok := tx.Liquid(pos)
	if !ok {
		return false
	}
	if !b.CanFill(liquid) {
		// Only allow picking up liquid source blocks.
		return false
	}
	tx.SetLiquid(pos, nil)
	tx.PlaySound(pos.Vec3Centre(), sound.BucketFill{Liquid: liquid})

	ctx.NewItem = NewStack(Bucket{Content: LiquidBucketContent(liquid)}, 1)
	ctx.NewItemSurvivalOnly = true
	ctx.SubtractFromCount(1)
	return true
}

// EncodeItem ...
func (b Bucket) EncodeItem() (name string, meta int16) {
	if !b.Empty() {
		return "minecraft:" + b.Content.String() + "_bucket", 0
	}
	return "minecraft:bucket", 0
}

type replaceable interface {
	ReplaceableBy(b world.Block) bool
}

func replaceableWith(b world.Block, with world.Block) bool {
	if r, ok := b.(replaceable); ok {
		return r.ReplaceableBy(with)
	}
	return false
}

func canDisplace(b world.Block, liq world.Liquid) bool {
	if d, ok := b.(world.LiquidDisplacer); ok {
		return d.CanDisplace(liq)
	}
	return false
}

// CanFill reports whether this empty bucket can collect the supplied liquid.
// Flowing and falling liquids cannot be collected.
func (b Bucket) CanFill(liquid world.Liquid) bool {
	return b.Empty() && liquid != nil && liquid.LiquidDepth() == 8 && !liquid.LiquidFalling()
}

// BucketPickupSource supplies terrain, both liquid layers, and loaded coverage.
type BucketPickupSource interface {
	world.BlockSource
	Liquid(cube.Pos) (world.Liquid, bool)
	BlockAvailable(cube.Pos) bool
}

// PickupTarget finds a visible source-liquid face on pos within reach of eye.
// It tries the centre and each face so a shallow pool's rim cannot hide a
// reachable top face. Unavailable terrain, intervening sources and block models
// before the hit are rejected. This conservative query does not check permission.
func (b Bucket) PickupTarget(pos cube.Pos, src BucketPickupSource, bounds cube.Range, eye mgl64.Vec3, reach float64) (trace.BBoxResult, bool) {
	centre := pos.Vec3Centre()
	best := reach * reach
	var chosen trace.BBoxResult
	found := false
	points := []mgl64.Vec3{centre}
	for _, face := range cube.Faces() {
		points = append(points, centre.Add(cube.Pos{}.Side(face).Vec3().Mul(.5)))
	}
	for _, aim := range points {
		hit, ok := b.pickupTargetRay(pos, src, bounds, eye, reach, aim)
		if distance := hit.Position().Sub(eye).LenSqr(); ok && (!found || distance < best) {
			chosen, best, found = hit, distance, true
		}
	}
	return chosen, found
}

// pickupTargetRay validates one candidate face ray against the source and terrain.
func (b Bucket) pickupTargetRay(pos cube.Pos, src BucketPickupSource, bounds cube.Range, eye mgl64.Vec3, reach float64, end mgl64.Vec3) (trace.BBoxResult, bool) {
	var none trace.BBoxResult
	if !(reach > 0) || !trace.ValidBlockSegment(bounds, eye, end) || !src.BlockAvailable(pos) {
		return none, false
	}
	liquid, ok := src.Liquid(pos)
	if !ok || !b.CanFill(liquid) {
		return none, false
	}
	box := cube.Box(0, 0, 0, 1, 1, 1).Translate(pos.Vec3())
	if box.Vec3Within(eye) {
		// A submerged eye needs an exit-face ray; a segment ending at the centre
		// never crosses a face. A centred eye uses the top face.
		direction := end.Sub(eye)
		if direction.LenSqr() < 1e-12 {
			direction = mgl64.Vec3{0, 1, 0}
		}
		end = eye.Add(direction.Normalize().Mul(2))
		if !trace.ValidBlockSegment(bounds, eye, end) {
			return none, false
		}
	}
	hit, ok := trace.BBoxIntercept(box, eye, end)
	if !ok || !(hit.Position().Sub(eye).LenSqr() <= reach*reach) {
		return none, false
	}
	clear := true
	trace.TraverseBlocks(eye, end, func(cell cube.Pos) bool {
		if cell.OutOfBounds(bounds) || !src.BlockAvailable(cell) {
			clear = false
			return false
		}
		if solid, blocked := trace.BlockIntercept(cell, src, src.Block(cell), eye, end); blocked && solid.Position().Sub(eye).LenSqr() <= hit.Position().Sub(eye).LenSqr() {
			clear = false
			return false
		}
		if cell == pos {
			return false
		}
		if other, wet := src.Liquid(cell); wet && b.CanFill(other) {
			clear = false
			return false
		}
		return true
	})
	return hit, clear
}
