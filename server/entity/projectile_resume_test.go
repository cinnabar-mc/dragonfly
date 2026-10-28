package entity

import (
	"math"
	"testing"

	"github.com/df-mc/dragonfly/server/block"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/go-gl/mathgl/mgl64"
)

// projectileFlightPositions returns the position after each of the first ticks
// of an unobstructed flight, as PredictProjectileLanding simulates it.
func projectileFlightPositions(t *testing.T, start, velocity mgl64.Vec3, physics ProjectilePhysics, ticks int) []mgl64.Vec3 {
	t.Helper()
	positions := make([]mgl64.Vec3, ticks)
	for i := range positions {
		got, ok := PredictProjectileLanding(pearlPredictionBlocks{}, func(cube.Pos) bool { return true }, start, velocity, physics, i+1)
		if !ok || got.Hit {
			t.Fatalf("open flight tick %d = %+v, %t", i+1, got, ok)
		}
		positions[i] = got.Position
	}
	return positions
}

func TestResumeProjectileFlight_ContinuesTheDeclaredFlight(t *testing.T) {
	start, velocity := mgl64.Vec3{0.5, 70.5, 0.5}, mgl64.Vec3{0.2, 0.6, 1.5}
	wall := pearlPredictionBlocks{}
	for y := 60; y <= 80; y++ {
		wall[cube.Pos{1, y, 15}] = block.Stone{}
		wall[cube.Pos{2, y, 15}] = block.Stone{}
	}
	for _, physics := range []ProjectilePhysics{
		{Gravity: 0.025},
		{Gravity: 0.03, Drag: 0.01, ForcesBeforeMove: true},
	} {
		full, ok := PredictProjectileLanding(wall, func(cube.Pos) bool { return true }, start, velocity, physics, 100)
		if !ok || !full.Hit {
			t.Fatalf("%+v: full flight = %+v, %t", physics, full, ok)
		}
		for tick, observed := range projectileFlightPositions(t, start, velocity, physics, 8) {
			resumed, flown, ok := ResumeProjectileFlight(start, velocity, observed, physics, 100)
			if !ok || flown != tick+1 {
				t.Fatalf("%+v: tick %d resumed after %d ticks, accepted = %t", physics, tick+1, flown, ok)
			}
			got, ok := PredictProjectileLanding(wall, func(cube.Pos) bool { return true }, observed, resumed, physics, 100)
			if !ok || got != full {
				t.Fatalf("%+v: resumed at tick %d = %+v, %t; want %+v", physics, tick+1, got, ok, full)
			}
		}
	}
}

func TestResumeProjectileFlight_IgnoresTerrainAlreadyPassed(t *testing.T) {
	start, velocity, physics := mgl64.Vec3{0.5, 70.5, 0.5}, mgl64.Vec3{0, 0, 1.5}, ProjectilePhysics{Gravity: 0.025}
	observed := projectileFlightPositions(t, start, velocity, physics, 4)[3]
	// A block placed behind the projectile must not become its predicted impact.
	blocks := pearlPredictionBlocks{{0, 70, 3}: block.Stone{}, {0, 69, 12}: block.Stone{}}
	if retraced, _ := PredictProjectileLanding(blocks, func(cube.Pos) bool { return true }, start, velocity, physics, 100); retraced.Block != (cube.Pos{0, 70, 3}) {
		t.Fatalf("setup: launch-state prediction = %+v, want the block behind", retraced)
	}
	resumed, _, ok := ResumeProjectileFlight(start, velocity, observed, physics, 100)
	if !ok {
		t.Fatal("resume rejected")
	}
	got, ok := PredictProjectileLanding(blocks, func(cube.Pos) bool { return true }, observed, resumed, physics, 100)
	if !ok || !got.Hit || got.Block != (cube.Pos{0, 69, 12}) {
		t.Fatalf("resumed prediction = %+v, %t; want the block ahead", got, ok)
	}
}

func TestResumeProjectileFlight_AtLaunchHasFlownNoTicks(t *testing.T) {
	start, velocity := mgl64.Vec3{0.5, 70.5, 0.5}, mgl64.Vec3{0, 0, 1.5}
	resumed, flown, ok := ResumeProjectileFlight(start, velocity, start, ProjectilePhysics{Gravity: 0.025}, 100)
	if !ok || flown != 0 || resumed != velocity {
		t.Fatalf("resume at launch = %v after %d ticks, accepted = %t", resumed, flown, ok)
	}
}

func TestResumeProjectileFlight_RejectsInvalidInputs(t *testing.T) {
	valid := mgl64.Vec3{0.5, 70.5, 0.5}
	for _, tt := range []struct {
		name     string
		observed mgl64.Vec3
		physics  ProjectilePhysics
		ticks    int
	}{
		{"no ticks", valid, ProjectilePhysics{}, 0},
		{"full drag", valid, ProjectilePhysics{Drag: 1}, 8},
		{"negative gravity", valid, ProjectilePhysics{Gravity: -1}, 8},
		{"non-finite", mgl64.Vec3{math.NaN()}, ProjectilePhysics{}, 8},
	} {
		if _, _, ok := ResumeProjectileFlight(valid, valid, tt.observed, tt.physics, tt.ticks); ok {
			t.Errorf("%s accepted", tt.name)
		}
	}
}

func TestResumeProjectileFlight_RejectsSimulationOverflow(t *testing.T) {
	limit := float64(math.MaxFloat32)
	for _, tt := range []struct {
		name     string
		start    mgl64.Vec3
		velocity mgl64.Vec3
		observed mgl64.Vec3
		physics  ProjectilePhysics
	}{
		{"continuation velocity", mgl64.Vec3{}, mgl64.Vec3{0, -limit, 0}, mgl64.Vec3{0, -limit, 0}, ProjectilePhysics{Gravity: limit}},
		{"position", mgl64.Vec3{limit, 0, 0}, mgl64.Vec3{limit, 0, 0}, mgl64.Vec3{limit, 0, 0}, ProjectilePhysics{}},
		{"forces before move", mgl64.Vec3{}, mgl64.Vec3{0, -limit, 0}, mgl64.Vec3{0, -limit, 0}, ProjectilePhysics{Gravity: limit, ForcesBeforeMove: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if velocity, flown, ok := ResumeProjectileFlight(tt.start, tt.velocity, tt.observed, tt.physics, 1); ok {
				t.Fatalf("overflowed simulation accepted: velocity %v after %d ticks", velocity, flown)
			}
		})
	}
}
