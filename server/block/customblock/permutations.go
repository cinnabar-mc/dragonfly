package customblock

import (
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/go-gl/mathgl/mgl64"
)

// Properties represents the different properties that can be applied to a block or a permutation.
type Properties struct {
	// CollisionBox represents the bounding box of the block that the player can collide with. This cannot exceed the
	// position of the current block in the world, otherwise it will be cut off at the edge.
	CollisionBox cube.BBox
	// DisableCollision explicitly disables collision, including in a permutation.
	// It takes precedence over CollisionBox. The zero value leaves the client's
	// default collision unchanged when CollisionBox is also empty.
	DisableCollision bool
	// Cube determines whether the block should inherit the default cube geometry. This will only be considered if the
	// Geometry field is empty.
	Cube bool
	// Geometry represents the geometry identifier that should be used for the block. If you want to use the default
	// cube geometry, leave this field empty and set Cube to true.
	Geometry string
	// GeometryCulling represents the identifier of the block-culling rules used by the geometry. Leave empty to use
	// the client's default culling behaviour.
	GeometryCulling string
	// GeometryCullingLayer groups this geometry with other blocks for culling rules that use the
	// same_culling_layer condition.
	GeometryCullingLayer string
	// BoneVisibility maps the names of bones in the geometry to a Molang expression that tells if the bone is shown.
	// The expression may only query block states, using query.block_state. Bones left out are always shown. It may
	// not be used with minecraft:geometry.full_block or minecraft:geometry.cross.
	BoneVisibility map[string]string
	// MapColour represents the hex colour that should be used for the block on a map.
	MapColour string
	// Rotation represents the rotation of the block. Rotations are only applied in 90 degree increments, meaning
	// 1 = 90 degrees, 2 = 180 degrees, 3 = 270 degrees and 4 = 360 degrees.
	Rotation cube.Pos
	// Scale is the scale of the block, with 1 being the default scale in all axes. When scaled, the block cannot
	// exceed a 30x30x30 pixel area otherwise the client will not render the block.
	Scale mgl64.Vec3
	// SelectionBox represents the bounding box of the block that the player can interact with. This cannot exceed the
	// position of the current block in the world, otherwise it will be cut off at the edge.
	SelectionBox cube.BBox
	// SelectionDisabled makes the block impossible to select, unless a permutation that holds sets a SelectionBox.
	// SelectionBox is then ignored.
	SelectionDisabled bool
	// Textures define the textures that should be used for the block. The key is the target of the texture, such as
	// "*" for all sides, or one of "up", "down", "north", "south", "east", "west" for a specific side.
	Textures map[string]Material
	// Translation is the translation of the block within itself. When translated, the block cannot exceed a 30x30x30
	// pixel area otherwise the client will not render the block.
	Translation mgl64.Vec3
}

// Permutation represents a specific permutation for a block that is only applied when the condition is met.
type Permutation struct {
	Properties
	// Condition is a molang query that is used to determine whether the permutation should be applied.
	// Only the latest version of molang is supported.
	Condition string
}
