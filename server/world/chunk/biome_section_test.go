package chunk

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/df-mc/dragonfly/server/block/cube"
)

// biomeSectionInput places every requested distinct nonzero value in a shuffled quart array.
func biomeSectionInput(count int, seed uint64) [4][4][4]uint32 {
	var biomes [4][4][4]uint32
	rng := rand.New(rand.NewPCG(seed, 42))
	for cell, value := range rng.Perm(64) {
		biomes[cell%4][cell/16][(cell/4)%4] = uint32(1 + value%count*7)
	}
	return biomes
}

// uniformBiomeSection returns a quart array filled with one biome ID.
func uniformBiomeSection(biome uint32) [4][4][4]uint32 {
	var biomes [4][4][4]uint32
	for x := range biomes {
		for y := range biomes[x] {
			for z := range biomes[x][y] {
				biomes[x][y][z] = biome
			}
		}
	}
	return biomes
}

// setBiomeSectionScalar uses the per-block setter in the converter's original quart-cell order.
func setBiomeSectionScalar(c *Chunk, baseY int16, biomes [4][4][4]uint32) {
	for qy := uint8(0); qy < 4; qy++ {
		for qz := uint8(0); qz < 4; qz++ {
			for qx := uint8(0); qx < 4; qx++ {
				biome := biomes[qx][qy][qz]
				for y := uint8(0); y < 4; y++ {
					for z := uint8(0); z < 4; z++ {
						for x := uint8(0); x < 4; x++ {
							c.SetBiome(qx*4+x, baseY+int16(qy*4+y), qz*4+z, biome)
						}
					}
				}
			}
		}
	}
}

// assertBiomeValues compares every block's biome, including negative world heights.
func assertBiomeValues(t *testing.T, got, want *Chunk) {
	t.Helper()
	for y := int16(want.Range()[0]); y <= int16(want.Range()[1]); y++ {
		for z := uint8(0); z < 16; z++ {
			for x := uint8(0); x < 16; x++ {
				if a, b := got.Biome(x, y, z), want.Biome(x, y, z); a != b {
					t.Fatalf("(%d,%d,%d): got %d, want %d", x, y, z, a, b)
				}
			}
		}
	}
}

// TestSetBiomeSectionScalarReference covers all 1..64 palette counts and all 98,304 cells per column.
func TestSetBiomeSectionScalarReference(t *testing.T) {
	for batch := range 3 {
		c := New(testBlockRegistry{}, cube.Range{-64, 319})
		want := New(testBlockRegistry{}, c.Range())
		for section := range c.biomes {
			count := 1 + (batch*24+section)%64
			biomes := biomeSectionInput(count, uint64(batch*24+section+1))
			if batch == 0 && section == 0 {
				biomes = uniformBiomeSection(0)
			}
			y := int16(-64 + section*16)
			c.SetBiome(3, y, 5, 999)
			// An interior block height must select the same section as its minimum height.
			c.SetBiomeSection(y+7, biomes)
			setBiomeSectionScalar(want, y, biomes)
			storage := c.biomes[section]
			size := paletteSizeFor(count)
			if storage.palette.Len() != count || storage.bitsPerIndex != uint16(size) || len(storage.indices) != size.uint32s() {
				t.Fatalf("count %d: palette=%d bits=%d words=%d", count, storage.palette.Len(), storage.bitsPerIndex, len(storage.indices))
			}
			if biomes[0][0][0] != 0 {
				for _, value := range storage.palette.values {
					if value == 0 {
						t.Fatalf("count %d: retained unused biome zero", count)
					}
				}
			}
		}
		assertBiomeValues(t, c, want)
	}
}

// TestSetBiomeSectionMutation checks input, cell, section and chunk independence at every palette width.
func TestSetBiomeSectionMutation(t *testing.T) {
	for _, count := range []int{0, 1, 2, 3, 5, 9, 17, 33, 64} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			biomes := uniformBiomeSection(0)
			if count != 0 {
				biomes = biomeSectionInput(count, 123)
			}
			c := New(testBlockRegistry{}, cube.Range{-64, -33})
			other := New(testBlockRegistry{}, c.Range())
			want := New(testBlockRegistry{}, c.Range())
			for _, y := range []int16{-64, -48} {
				c.SetBiomeSection(y, biomes)
				other.SetBiomeSection(y, biomes)
				setBiomeSectionScalar(want, y, biomes)
			}
			biomes[0][0][0] = 999
			assertBiomeValues(t, c, want)
			wantOther := want.Clone()
			c.SetBiome(3, -60, 5, 999)
			want.SetBiome(3, -60, 5, 999)
			assertBiomeValues(t, c, want)
			assertBiomeValues(t, other, wantOther)
		})
	}
}

// TestSetBiomeSectionEncoding checks every decoded cell through network and disk palette encodings.
func TestSetBiomeSectionEncoding(t *testing.T) {
	c := New(testBlockRegistry{}, cube.Range{-64, 319})
	counts := []int{1, 2, 3, 4, 5, 8, 9, 16, 17, 32, 33, 64}
	for section := range c.biomes {
		biomes := biomeSectionInput(counts[section%len(counts)], uint64(section+1))
		if section == 0 {
			biomes = uniformBiomeSection(0)
		}
		c.SetBiomeSection(int16(-64+section*16), biomes)
	}
	for _, encoding := range []Encoding{NetworkEncoding, DiskEncoding} {
		buf := bytes.NewBuffer(EncodeBiomes(c, encoding))
		var previous *PalettedStorage
		for section := range c.biomes {
			storage, err := decodePalettedStorage(buf, encoding, BiomePaletteEncoding)
			if err != nil {
				t.Fatal(err)
			}
			if storage == nil {
				storage = previous
			}
			if storage == nil {
				t.Fatal("first section referred to previous biome storage")
			}
			for y := uint8(0); y < 16; y++ {
				for z := uint8(0); z < 16; z++ {
					for x := uint8(0); x < 16; x++ {
						if got, want := storage.At(x, y, z), c.Biome(x, int16(-64+section*16)+int16(y), z); got != want {
							t.Fatalf("section %d (%d,%d,%d): got %d, want %d", section, x, y, z, got, want)
						}
					}
				}
			}
			previous = storage
		}
		if buf.Len() != 0 {
			t.Fatalf("%d trailing biome bytes", buf.Len())
		}
	}
}

// BenchmarkSetBiomeSection compares identical prepared quart columns through scalar and bulk assignment.
func BenchmarkSetBiomeSection(b *testing.B) {
	for _, count := range []int{0, 1, 3, 16, 64} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			var sections [24][4][4][4]uint32
			for section := range sections {
				if count != 0 {
					sections[section] = biomeSectionInput(count, uint64(section+1))
				}
			}
			for _, method := range []string{"scalar", "bulk"} {
				b.Run(method, func(b *testing.B) {
					var c *Chunk
					b.ReportAllocs()
					for b.Loop() {
						c = New(testBlockRegistry{}, cube.Range{-64, 319})
						for section, biomes := range sections {
							y := int16(-64 + section*16)
							if method == "scalar" {
								setBiomeSectionScalar(c, y, biomes)
							} else {
								c.SetBiomeSection(y, biomes)
							}
						}
					}
					b.ReportMetric(float64(len(EncodeBiomes(c, NetworkEncoding))), "biome-B/column")
				})
			}
		})
	}
}
