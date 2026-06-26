package main

// These tests reproduce the dashboard's accumulator transform (applyAccumulator
// in trv-outpost client/src/chart-spec/option-helpers.js) against the values
// this writer produces, proving each behavior drives the intended branch.

import "testing"

// reset policies, mirrored from the dashboard.
const (
	dropNegative = "drop_negative"
	keepNegative = "keep_negative"
	clampZero    = "clamp_zero"
)

// nullable mirrors a source y-value that may be missing (gap → present=false).
type nullable struct {
	v       int64
	present bool
}

// applyAccumulator is a faithful Go port of the dashboard JS, used only to
// assert this writer's output deltas the way the code under test will.
func applyAccumulator(vals []nullable, policy string) []*int64 {
	out := make([]*int64, len(vals))
	var prev *int64
	for i, n := range vals {
		if !n.present {
			out[i] = nil // gap; keep prev (don't reset)
			continue
		}
		cur := n.v
		if prev == nil {
			out[i] = nil
			p := cur
			prev = &p
			continue
		}
		delta := cur - *prev
		p := cur
		prev = &p
		if delta < 0 {
			switch policy {
			case keepNegative:
				d := delta
				out[i] = &d
			case clampZero:
				z := int64(0)
				out[i] = &z
			default: // dropNegative
				out[i] = nil
			}
		} else {
			d := delta
			out[i] = &d
		}
	}
	return out
}

// drive runs a counter through a fixed behavior sequence and returns the
// per-tick (value, present) the writer would emit.
func drive(c *Counter, behaviors []Behavior) []nullable {
	out := make([]nullable, len(behaviors))
	for i, b := range behaviors {
		c.trigger(b)
		v, emit := c.tick()
		out[i] = nullable{v: v, present: emit}
	}
	return out
}

func TestRampProducesPositiveDeltas(t *testing.T) {
	c := &Counter{Name: "x", Step: 100, SpikeMult: 10}
	seq := drive(c, []Behavior{BehaviorRamp, BehaviorRamp, BehaviorRamp, BehaviorRamp})
	d := applyAccumulator(seq, dropNegative)
	// index 0 is always null; the rest should be +100 each.
	if d[0] != nil {
		t.Fatalf("index 0 should be null, got %v", *d[0])
	}
	for i := 1; i < len(d); i++ {
		if d[i] == nil || *d[i] != 100 {
			t.Fatalf("tick %d: want delta 100, got %v", i, d[i])
		}
	}
}

func TestResetDropsLineUnderDropNegative(t *testing.T) {
	c := &Counter{Name: "x", Step: 50, SpikeMult: 10}
	seq := drive(c, []Behavior{BehaviorRamp, BehaviorRamp, BehaviorReset, BehaviorRamp})
	// After two ramps value=100; reset → value=Step(50): delta = 50-100 = -50.
	if got := applyAccumulator(seq, dropNegative)[2]; got != nil {
		t.Fatalf("drop_negative reset: want null, got %v", *got)
	}
	if got := applyAccumulator(seq, clampZero)[2]; got == nil || *got != 0 {
		t.Fatalf("clamp_zero reset: want 0, got %v", got)
	}
	if got := applyAccumulator(seq, keepNegative)[2]; got == nil || *got >= 0 {
		t.Fatalf("keep_negative reset: want negative, got %v", got)
	}
}

func TestWrapIsNegativeDelta(t *testing.T) {
	c := &Counter{Name: "x", Step: 300, Max: 100000, SpikeMult: 10}
	// Ramp once, then wrap. Wrap lands on a small remainder → delta < 0.
	seq := drive(c, []Behavior{BehaviorRamp, BehaviorRamp, BehaviorWrap})
	if got := applyAccumulator(seq, keepNegative)[2]; got == nil || *got >= 0 {
		t.Fatalf("wrap should yield negative delta under keep_negative, got %v", got)
	}
}

func TestFlatIsZeroDelta(t *testing.T) {
	c := &Counter{Name: "x", Step: 10, SpikeMult: 10}
	seq := drive(c, []Behavior{BehaviorRamp, BehaviorRamp, BehaviorFlat})
	if got := applyAccumulator(seq, dropNegative)[2]; got == nil || *got != 0 {
		t.Fatalf("flat: want delta 0, got %v", got)
	}
}

func TestSpikeIsLargePositiveDelta(t *testing.T) {
	c := &Counter{Name: "x", Step: 10, SpikeMult: 50}
	seq := drive(c, []Behavior{BehaviorRamp, BehaviorSpike})
	got := applyAccumulator(seq, dropNegative)[1]
	if got == nil || *got != 10*50 {
		t.Fatalf("spike: want delta %d, got %v", 10*50, got)
	}
}

// The critical one: a GAP must not read as a counter reset. After the gap the
// next real value deltas against the PRE-gap value, yielding a normal positive
// delta — never a spurious negative/null from treating the hole as a drop.
func TestGapDoesNotTriggerReset(t *testing.T) {
	c := &Counter{Name: "x", Step: 100, SpikeMult: 10}
	seq := drive(c, []Behavior{BehaviorRamp, BehaviorGap, BehaviorRamp})
	out := applyAccumulator(seq, dropNegative)
	if out[1] != nil {
		t.Fatalf("gap tick should be null, got %v", *out[1])
	}
	// tick 0 value=100, gap holds, tick 2 ramps to 200 → delta vs 100 = +100.
	if out[2] == nil || *out[2] != 100 {
		t.Fatalf("post-gap delta: want +100 (vs pre-gap value), got %v", out[2])
	}
}
