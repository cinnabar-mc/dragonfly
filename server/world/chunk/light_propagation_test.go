package chunk

import (
	"fmt"
	"testing"

	"github.com/df-mc/dragonfly/server/block/cube"
)

func TestTerminalLightPropagationDoesNotInspectNeighbours(t *testing.T) {
	for name, lt := range map[string]light{"sky": SkyLight, "block": BlockLight} {
		t.Run(name, func(t *testing.T) {
			br := &countingLightRegistry{}
			a := newPropagationTestArea(br)
			pos := cube.Pos{8, -8, 8}
			queue := newLightQueue(1)
			queue.push(node(pos, 1, lt))

			a.propagate(queue)

			if got := a.light(pos, lt); got != 1 {
				t.Fatalf("light at source = %d, want 1", got)
			}
			if !queue.empty() {
				t.Fatal("level-one light must not spread to neighbours")
			}
			if br.filterCalls != 0 {
				t.Fatalf("filtering lookups = %d, want 0 for light that cannot spread", br.filterCalls)
			}
		})
	}
}

func TestLightPropagationAttenuatesToRangeEdges(t *testing.T) {
	for name, lt := range map[string]light{"sky": SkyLight, "block": BlockLight} {
		for _, level := range []uint8{1, 2, 15} {
			t.Run(fmt.Sprintf("%s/%d", name, level), func(t *testing.T) {
				a := newPropagationTestArea(testBlockRegistry{})
				pos := cube.Pos{8, -8, 8}
				queue := newLightQueue(16)
				queue.push(node(pos, level, lt))
				for !queue.empty() {
					a.propagate(queue)
				}

				for x := 0; x < 16; x++ {
					for y := a.r.Min(); y <= a.r.Max(); y++ {
						for z := 0; z < 16; z++ {
							p := cube.Pos{x, y, z}
							distance := 0
							for i := range p {
								distance += max(p[i]-pos[i], pos[i]-p[i])
							}
							want := uint8(max(0, int(level)-distance))
							if got := a.light(p, lt); got != want {
								t.Fatalf("light at %v = %d, want %d", p, got, want)
							}
						}
					}
				}
			})
		}
	}
}

func BenchmarkLightPropagation(b *testing.B) {
	for name, lt := range map[string]light{"sky": SkyLight, "block": BlockLight} {
		b.Run(name, func(b *testing.B) {
			a := newPropagationTestArea(testBlockRegistry{})
			queue := newLightQueue(initialLightQueueCapacity)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, sub := range a.c[0].sub {
					sub.skyLight, sub.blockLight = noLight, noLight
				}
				queue.push(node(cube.Pos{8, -8, 8}, 15, lt))
				for !queue.empty() {
					a.propagate(queue)
				}
			}
		})
	}
}

// newPropagationTestArea creates a dark column with storage for filtering lookups.
func newPropagationTestArea(br BlockRegistry) *lightArea {
	c := New(br, cube.Range{-16, 15})
	for _, sub := range c.sub {
		sub.Layer(0)
		sub.skyLight, sub.blockLight = noLight, noLight
	}
	return LightArea([]*Chunk{c}, 0, 0)
}

type countingLightRegistry struct {
	testBlockRegistry
	filterCalls int
}

// FilteringBlock counts the block-filter lookups made during propagation.
func (r *countingLightRegistry) FilteringBlock(uint32) uint8 {
	r.filterCalls++
	return 0
}
