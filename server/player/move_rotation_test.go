package player

import (
	"testing"

	"github.com/df-mc/dragonfly/server/world"
	"github.com/go-gl/mathgl/mgl64"
)

// TestMoveKeepsVerticalPitch checks that moving to either vertical endpoint, and turning while
// there, keeps the pitch instead of wrapping it to the opposite endpoint. Pitch past an endpoint
// stops at it, while yaw keeps wrapping around.
func TestMoveKeepsVerticalPitch(t *testing.T) {
	w := world.Config{Synchronous: true}.New()
	defer w.Close()
	handle := world.EntitySpawnOpts{Position: mgl64.Vec3{0, 100, 0}}.New(Type, Config{Name: "Mover"})
	w.Do(func(tx *world.Tx) {
		p := tx.AddEntity(handle).(*Player)
		for _, tc := range []struct {
			name                 string
			deltaYaw, deltaPitch float64
			yaw, pitch           float64
		}{
			{"straight down", 0, 90, 0, 90},
			{"turn while looking down", 45, 0, 45, 90},
			{"straight up", 0, -180, 45, -90},
			{"turn while looking up", -90, 0, -45, -90},
			{"past straight down", 135, 195, 90, 90},
			{"past straight up", 45, -195, 135, -90},
			{"yaw wraps", 55, 100, -170, 10},
		} {
			p.Move(mgl64.Vec3{}, tc.deltaYaw, tc.deltaPitch)
			if got := p.Rotation(); !mgl64.FloatEqual(got.Yaw(), tc.yaw) || got.Pitch() != tc.pitch {
				t.Errorf("%s: rotation after Move = %v; want yaw %v, pitch %v", tc.name, got, tc.yaw, tc.pitch)
			}
		}
	})
}
