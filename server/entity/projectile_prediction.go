package entity

import (
	"math"

	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/block/cube/trace"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl32"
	"github.com/go-gl/mathgl/mgl64"
)

const projectileRadiansToIndex float32 = 10430.3779296875

// ProjectileLaunchVelocity returns the Bedrock projectile launch velocity for
// rotation, speed, and the projectile definition's angle offset. Bedrock uses indexed, single-precision trigonometry;
// Rotation.Vec3 uses continuous, double-precision trigonometry instead.
// Values outside finite single precision return a non-finite velocity that
// PredictProjectileLanding rejects.
func ProjectileLaunchVelocity(rotation cube.Rotation, speed, angleOffset float64) mgl64.Vec3 {
	const (
		degreesToRadians float32 = -0.01745329238474369
		negativePi       float32 = -3.1415927410125732
		quarterTurn      float32 = 16384
	)
	for _, value := range []float64{rotation.Yaw(), rotation.Pitch(), speed, angleOffset} {
		if !projectileFinite32(value) {
			return mgl64.Vec3{math.NaN(), math.NaN(), math.NaN()}
		}
	}
	pitch := float32(rotation.Pitch())
	// Since Bedrock 1.20.20, the angle offset is dampened towards vertical
	// aim using the positive-pitch indexed cosine before adjusting pitch.
	if angleOffset != 0 {
		index := float32(float32(pitch*-degreesToRadians) * projectileRadiansToIndex)
		pitch += float32(angleOffset) * projectileIndexedSin(index+quarterTurn)
	}
	yawRadians := float32(float32(rotation.Yaw()) * degreesToRadians)
	pitchRadians := float32(pitch * degreesToRadians)
	yawIndex := float32(float32(yawRadians+negativePi) * projectileRadiansToIndex)
	pitchIndex := float32(pitchRadians * projectileRadiansToIndex)
	cosPitch := projectileIndexedSin(pitchIndex + quarterTurn)
	direction := mgl32.Vec3{
		projectileIndexedSin(yawIndex) * -cosPitch,
		projectileIndexedSin(pitchIndex),
		projectileIndexedSin(yawIndex+quarterTurn) * -cosPitch,
	}
	// Keep each product and sum at single precision, including the reciprocal
	// multiply used by Bedrock's projectile normalization.
	xy := float32(float32(direction[0]*direction[0]) + float32(direction[1]*direction[1]))
	lengthSquared := float32(xy + float32(direction[2]*direction[2]))
	length := float32(math.Sqrt(float64(lengthSquared)))
	if length < 0.001 {
		return mgl64.Vec3{}
	}
	return projectileVec64(direction.Mul(1 / length).Mul(float32(speed)))
}

func projectileIndexedSin(index float32) float32 {
	// The Bedrock sine table stores sinf(float32(i)/projectileRadiansToIndex).
	// Evaluate only the requested entry instead of retaining a global table.
	angle := float32(uint32(int32(index))&65535) / projectileRadiansToIndex
	return float32(math.Sin(float64(angle)))
}

func projectileVec64(v mgl32.Vec3) mgl64.Vec3 {
	return mgl64.Vec3{float64(v[0]), float64(v[1]), float64(v[2])}
}

func projectileFinite32(value float64) bool {
	return math.Abs(value) <= math.MaxFloat32
}

// ProjectilePhysics contains launch speed and the per-tick forces used by a
// projectile. Drag reduces each velocity component by its fraction before
// gravity is subtracted from vertical velocity.
type ProjectilePhysics struct {
	Speed, Gravity, Drag float64
	// ForcesBeforeMove applies those forces before each displacement, as native
	// Dragonfly projectiles do. The zero value moves first, matching BDS.
	ForcesBeforeMove bool
}

// ProjectilePrediction is the first block hit of a projectile, or the
// last position reached before its flight leaves available terrain or its tick
// budget. Hit is false for the latter case; Block and Face are then undefined.
type ProjectilePrediction struct {
	Position mgl64.Vec3
	Block    cube.Pos
	Face     cube.Face
	Hit      bool
}

// PredictProjectileLanding predicts the first block hit of a projectile.
// The caller supplies the initial velocity and per-tick forces, allowing servers
// with custom projectile physics to use their own values. When the flight reaches
// unavailable terrain or the tick budget, the last known position is returned
// without a block hit. It returns false only when the inputs are invalid or no
// complete flight step could be simulated.
// ForcesBeforeMove selects whether drag and gravity precede displacement.
// Its zero value uses the current velocity before applying the following tick's
// forces, matching Bedrock Dedicated Server projectiles.
// Position, velocity, and per-tick forces use Bedrock's single precision.
// Moving entities are intentionally outside this block-only prediction.
func PredictProjectileLanding(src world.BlockSource, available func(cube.Pos) bool, start, velocity mgl64.Vec3, physics ProjectilePhysics, maxTicks int) (ProjectilePrediction, bool) {
	gravity, drag := physics.Gravity, physics.Drag
	if src == nil || available == nil || maxTicks <= 0 || !projectileFinite32(gravity) || !projectileFinite32(drag) || gravity < 0 || drag < 0 || drag >= 1 {
		return ProjectilePrediction{}, false
	}
	for _, v := range []mgl64.Vec3{start, velocity} {
		for _, component := range v {
			if !projectileFinite32(component) {
				return ProjectilePrediction{}, false
			}
		}
	}
	pos := mgl32.Vec3{float32(start[0]), float32(start[1]), float32(start[2])}
	motion := mgl32.Vec3{float32(velocity[0]), float32(velocity[1]), float32(velocity[2])}
	inertia, gravity32 := float32(1-drag), float32(gravity)
	if physics.ForcesBeforeMove {
		motion = projectileNextVelocity(motion, inertia, gravity32)
	}
	advanced := false
	for range maxTicks {
		end := pos.Add(motion)
		from, to := projectileVec64(pos), projectileVec64(end)
		var hit trace.BlockResult
		found, known := false, true
		if mgl64.FloatEqual(to.Sub(from).LenSqr(), 0) {
			// A vertical throw may have a stationary tick at its apex. Do not
			// stop its flight before gravity starts its descent.
			known = available(cube.PosFromVec3(from))
		} else {
			trace.TraverseBlocks(from, to, func(cell cube.Pos) bool {
				if !available(cell) {
					known = false
					return false
				}
				if result, ok := trace.BlockIntercept(cell, src, src.Block(cell), from, to); ok {
					hit, found = result, true
					return false
				}
				return true
			})
		}
		if !known {
			return ProjectilePrediction{Position: from}, advanced
		}
		if found {
			point := hit.Position()
			return ProjectilePrediction{Position: mgl64.Vec3{float64(float32(point[0])), float64(float32(point[1])), float64(float32(point[2]))}, Block: hit.BlockPosition(), Face: hit.Face(), Hit: true}, true
		}
		pos = end
		advanced = true
		motion = projectileNextVelocity(motion, inertia, gravity32)
	}
	return ProjectilePrediction{Position: projectileVec64(pos)}, advanced
}

// ResumeProjectileFlight locates a projectile partway through the flight
// launched from start with velocity. It returns the velocity to pass to
// PredictProjectileLanding together with the observed position, so the
// prediction covers only the remaining flight instead of retracing cells the
// projectile has already passed, and the number of ticks already flown, so a
// caller can keep the whole flight within one tick budget. The flight tick
// nearest observed among the first maxTicks is chosen; collisions are ignored,
// so observed must belong to the same uninterrupted flight. A flight that
// revisits a point, such as a vertical throw, resolves to the earliest nearest
// tick. It returns false when the inputs are invalid or the simulation leaves
// the finite float32 range.
func ResumeProjectileFlight(start, velocity, observed mgl64.Vec3, physics ProjectilePhysics, maxTicks int) (mgl64.Vec3, int, bool) {
	gravity, drag := physics.Gravity, physics.Drag
	if maxTicks <= 0 || !projectileFinite32(gravity) || !projectileFinite32(drag) || gravity < 0 || drag < 0 || drag >= 1 {
		return mgl64.Vec3{}, 0, false
	}
	for _, v := range []mgl64.Vec3{start, velocity, observed} {
		for _, component := range v {
			if !projectileFinite32(component) {
				return mgl64.Vec3{}, 0, false
			}
		}
	}
	pos := mgl32.Vec3{float32(start[0]), float32(start[1]), float32(start[2])}
	declared := mgl32.Vec3{float32(velocity[0]), float32(velocity[1]), float32(velocity[2])}
	inertia, gravity32 := float32(1-drag), float32(gravity)
	best, bestTick, bestDistance := declared, 0, projectileVec64(pos).Sub(observed).LenSqr()
	for tick := 1; tick <= maxTicks; tick++ {
		// Mirror PredictProjectileLanding's force phase so the returned velocity
		// continues the flight exactly from the matched tick.
		motion := declared
		if physics.ForcesBeforeMove {
			motion = projectileNextVelocity(declared, inertia, gravity32)
		}
		pos = pos.Add(motion)
		declared = projectileNextVelocity(declared, inertia, gravity32)
		for axis := range pos {
			if !projectileFinite32(float64(pos[axis])) || !projectileFinite32(float64(declared[axis])) {
				return mgl64.Vec3{}, 0, false
			}
		}
		if distance := projectileVec64(pos).Sub(observed).LenSqr(); distance < bestDistance {
			best, bestTick, bestDistance = declared, tick, distance
		}
	}
	return projectileVec64(best), bestTick, true
}

func projectileNextVelocity(motion mgl32.Vec3, inertia, gravity float32) mgl32.Vec3 {
	for axis := range motion {
		motion[axis] = float32(motion[axis] * inertia)
	}
	motion[1] -= gravity
	return motion
}
