package blockinternal

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/block/customblock"
	"github.com/df-mc/dragonfly/server/item"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

// Components returns all the components for the custom block, including permutations and properties.
func Components(identifier string, b world.CustomBlock, blockID int32) map[string]any {
	components := componentsFromProperties(identifier, b.Properties())
	builder := NewComponentBuilder(identifier, components, blockID)
	if emitter, ok := b.(block.LightEmitter); ok {
		builder.AddComponent("minecraft:light_emission", map[string]any{
			"emission": int32(emitter.LightEmissionLevel()),
		})
	}
	if diffuser, ok := b.(block.LightDiffuser); ok {
		builder.AddComponent("minecraft:light_dampening", map[string]any{
			"lightLevel": int32(diffuser.LightDiffusionLevel()),
		})
	}
	if breakable, ok := b.(block.Breakable); ok {
		// The value is the seconds the client takes to destroy the block, not its hardness, and -1
		// is its sentinel for a block mining never destroys. Bedrock has no per-tool speed
		// component, so the bare-handed duration is the only one a static definition can carry, and
		// it stays unrounded: the client turns the value back into a hardness and quantises itself.
		info := breakable.BreakInfo()
		seconds := info.Hardness * 30 / 20
		switch {
		case info.Hardness < 0:
			seconds = -1
		case !info.Harvestable(item.ToolNone{}):
			seconds = info.Hardness * 100 / 20
		}
		builder.AddComponent("minecraft:destructible_by_mining", map[string]any{"value": float32(seconds)})
	}
	if frictional, ok := b.(block.Frictional); ok {
		builder.AddComponent("minecraft:friction", map[string]any{"value": float32(frictional.Friction())})
	}
	if flammable, ok := b.(block.Flammable); ok {
		info := flammable.FlammabilityInfo()
		builder.AddComponent("minecraft:flammable", map[string]any{
			"flame_odds": int32(info.Encouragement),
			"burn_odds":  int32(info.Flammability),
		})
	}
	if permutable, ok := b.(block.Permutable); ok {
		// The client adds properties in the order of the list, which orders the block's runtime IDs, so
		// they are sent sorted by name, the order the registry gives the states runtime IDs in.
		states := permutable.States()
		for _, name := range slices.Sorted(maps.Keys(states)) {
			builder.AddProperty(name, states[name])
		}
		for _, permutation := range permutable.Permutations() {
			builder.AddPermutation(permutation.Condition, componentsFromProperties(identifier, permutation.Properties))
		}
	}
	if traited, ok := b.(block.Traited); ok {
		traits, err := customblock.SortTraits(traited.Traits())
		if err != nil {
			panic(fmt.Sprintf("custom block %s: %v", identifier, err))
		}
		for _, trait := range traits {
			builder.AddTrait(trait)
		}
	}
	if item, ok := b.(world.CustomItem); ok {
		builder.SetMenuCategory(item.Category())
	}
	if tagged, ok := b.(block.Tagged); ok {
		builder.SetTags(tagged.Tags()...)
	}
	return builder.Construct()
}

// componentsFromProperties builds a base components map that includes all the common data between a regular block and
// a custom permutation.
func componentsFromProperties(identifier string, props customblock.Properties) map[string]any {
	components := make(map[string]any)
	if props.DisableCollision {
		components["minecraft:collision_box"] = map[string]any{"enabled": false}
	} else if props.CollisionBox != (cube.BBox{}) {
		components["minecraft:collision_box"] = collisionBoxComponent(props.CollisionBox)
	}
	switch {
	case props.SelectionDisabled:
		components["minecraft:selection_box"] = map[string]any{"enabled": false}
	case props.SelectionBox != (cube.BBox{}):
		components["minecraft:selection_box"] = selectionBoxComponent(props.SelectionBox)
	}
	geometry := props.Geometry
	if geometry == "" && props.Cube {
		geometry = "minecraft:geometry.full_block"
	}
	if geometry != "" {
		component := map[string]any{"identifier": geometry}
		if props.GeometryCulling != "" {
			component["culling"] = props.GeometryCulling
		}
		if props.GeometryCullingLayer != "" {
			component["culling_layer"] = props.GeometryCullingLayer
		}
		if len(props.BoneVisibility) > 0 {
			if geometry == "minecraft:geometry.full_block" || geometry == "minecraft:geometry.cross" {
				panic(fmt.Sprintf("custom block %s: bone visibility cannot be used with %s", identifier, geometry))
			}
			visibility := make(map[string]any, len(props.BoneVisibility))
			for bone, expression := range props.BoneVisibility {
				visibility[bone] = boneVisibilityExpression(expression)
			}
			component["bone_visibility"] = visibility
		}
		components["minecraft:geometry"] = component
	}
	if props.MapColour != "" {
		components["minecraft:map_color"] = map[string]any{"value": props.MapColour}
	}
	if props.Textures != nil {
		components["minecraft:material_instances"] = props.MaterialInstances()
	}
	transformation := make(map[string]any)
	if props.Rotation != (cube.Pos{}) {
		transformation["RX"] = int32(props.Rotation.X())
		transformation["RY"] = int32(props.Rotation.Y())
		transformation["RZ"] = int32(props.Rotation.Z())
	}
	if props.Translation != (mgl64.Vec3{}) {
		transformation["TX"] = float32(props.Translation.X())
		transformation["TY"] = float32(props.Translation.Y())
		transformation["TZ"] = float32(props.Translation.Z())
	}
	if props.Scale != (mgl64.Vec3{}) {
		transformation["SX"] = float32(props.Scale.X())
		transformation["SY"] = float32(props.Scale.Y())
		transformation["SZ"] = float32(props.Scale.Z())
	} else if len(transformation) > 0 {
		transformation["SX"] = float32(1.0)
		transformation["SY"] = float32(1.0)
		transformation["SZ"] = float32(1.0)
	}
	if len(transformation) > 0 {
		components["minecraft:transformation"] = transformation
	}
	return components
}

// boneVisibilityExpression returns the expression as vanilla sends it: a constant is written with six decimals.
func boneVisibilityExpression(expression string) string {
	if v, err := strconv.ParseFloat(strings.TrimSpace(expression), 32); err == nil {
		return strconv.FormatFloat(v, 'f', 6, 32)
	}
	return expression
}

// collisionBoxComponent returns the component data for a collision box, using absolute min/max coordinates in pixels.
func collisionBoxComponent(box cube.BBox) map[string]any {
	min, max := box.Min(), box.Max()
	return map[string]any{
		"enabled": true,
		"boxes": []map[string]any{
			{
				"minX": float32(min.X() * 16),
				"minY": float32(min.Y() * 16),
				"minZ": float32(min.Z() * 16),
				"maxX": float32(max.X() * 16),
				"maxY": float32(max.Y() * 16),
				"maxZ": float32(max.Z() * 16),
			},
		},
	}
}

// selectionBoxComponent returns the component data for a selection box, translating coordinates to the origin/size
// format the client expects.
func selectionBoxComponent(box cube.BBox) map[string]any {
	min, max := box.Min(), box.Max()
	originX, originY, originZ := min.X()*16, min.Y()*16, min.Z()*16
	sizeX, sizeY, sizeZ := (max.X()-min.X())*16, (max.Y()-min.Y())*16, (max.Z()-min.Z())*16
	return map[string]any{
		"enabled": true,
		"origin":  []float32{float32(originX) - 8, float32(originY), float32(originZ) - 8},
		"size":    []float32{float32(sizeX), float32(sizeY), float32(sizeZ)},
	}
}
