package main

import (
	"math/rand"
	"sync"
)

// Behavior is a one-shot transformation applied to a counter on the NEXT tick.
// Counters normally ramp upward (positive delta). A behavior forces one of the
// edge cases the dashboard's accumulator transform must handle — see
// applyAccumulator in trv-outpost client/src/chart-spec/option-helpers.js:
//
//	ramp   → positive delta            (the common case)
//	wrap   → value overflows past Max, wrapping to a small value (delta < 0)
//	reset  → value snaps back to 0     (delta < 0; counter restart)
//	spike  → one huge positive jump    (large positive delta)
//	flat   → value unchanged this tick (delta == 0)
//	gap    → no value emitted (null)   (must NOT count as a reset downstream)
//
// wrap/reset both yield delta<0, exercising drop_negative / keep_negative /
// clamp_zero. flat proves delta==0 stays on the line. gap proves a null hole
// doesn't trigger the reset branch (prev is preserved across the gap).
type Behavior string

const (
	BehaviorRamp  Behavior = "ramp"
	BehaviorWrap  Behavior = "wrap"
	BehaviorReset Behavior = "reset"
	BehaviorSpike Behavior = "spike"
	BehaviorFlat  Behavior = "flat"
	BehaviorGap   Behavior = "gap"
)

func validBehavior(b Behavior) bool {
	switch b {
	case BehaviorRamp, BehaviorWrap, BehaviorReset, BehaviorSpike, BehaviorFlat, BehaviorGap:
		return true
	}
	return false
}

// Counter is one monotonically-increasing column. It owns its current value and
// any pending one-shot behavior. Concurrency: tick() runs on the writer loop;
// trigger() runs on the HTTP handler — both guarded by mu.
type Counter struct {
	Name string
	// Step is the per-tick increment for a normal ramp.
	Step int64
	// Max is the wrap ceiling. A wrap sets value to value%Max-ish small remainder.
	Max int64
	// SpikeMult multiplies Step for a spike tick.
	SpikeMult int64
	// AutoEvery: if >0, the counter auto-fires AutoBehavior every Nth tick (a
	// scheduled behavior, so the source self-exercises even with no HTTP poking).
	// 0 disables the schedule for this counter.
	AutoEvery     int
	AutoBehavior  Behavior

	mu      sync.Mutex
	value   int64
	ticks   int
	pending Behavior // one-shot override for the next tick ("" = none)
}

// trigger queues a one-shot behavior for the next tick. Returns false for an
// unknown behavior so the HTTP layer can 400.
func (c *Counter) trigger(b Behavior) bool {
	if !validBehavior(b) {
		return false
	}
	c.mu.Lock()
	c.pending = b
	c.mu.Unlock()
	return true
}

// tick advances the counter one step and returns (value, emit). emit=false means
// this tick is a GAP — the caller must OMIT the field from the record so the
// dashboard sees a null/missing value (not a zero). A pending one-shot behavior
// takes precedence over the auto-schedule.
func (c *Counter) tick() (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.ticks++

	b := c.pending
	c.pending = ""
	if b == "" && c.AutoEvery > 0 && c.ticks%c.AutoEvery == 0 {
		b = c.AutoBehavior
	}
	if b == "" {
		b = BehaviorRamp
	}

	switch b {
	case BehaviorGap:
		// Hold the value; emit nothing. Next real tick deltas against the
		// pre-gap value, which is the behavior under test.
		return c.value, false

	case BehaviorFlat:
		// No change → delta 0 downstream.
		return c.value, true

	case BehaviorReset:
		// Counter restart: snap to a small base so the next ramp climbs again.
		c.value = c.Step
		return c.value, true

	case BehaviorWrap:
		// Overflow past Max. Land on a small remainder so the delta is sharply
		// negative (current - previous < 0), like a fixed-width counter rolling.
		c.value = (c.value % c.Step) + c.Step
		return c.value, true

	case BehaviorSpike:
		c.value += c.Step * c.SpikeMult
		return c.value, true

	default: // BehaviorRamp
		c.value += c.Step
		// Natural wrap when a ramp would exceed Max — keeps values bounded and
		// produces an organic reset even with no scheduled/triggered behavior.
		if c.Max > 0 && c.value >= c.Max {
			c.value = c.value % c.Step
		}
		return c.value, true
	}
}

// snapshot returns the current value without advancing (for /config & /health).
func (c *Counter) snapshot() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

// defaultCounters defines the test columns. Each targets a different edge of the
// accumulator transform; together they exercise every branch of applyAccumulator
// in one record. Names become ts-store field names (and dashboard y_axis columns).
func defaultCounters() []*Counter {
	return []*Counter{
		// Clean ever-rising odometer — the baseline positive-delta ramp.
		{Name: "bytes_total", Step: 1500 + rand.Int63n(500), Max: 0, SpikeMult: 20},

		// Wraps on a schedule (fixed-width counter rolling over) → delta<0.
		{Name: "packets_total", Step: 300, Max: 100000, SpikeMult: 15,
			AutoEvery: 37, AutoBehavior: BehaviorWrap},

		// Resets to zero on a schedule (service/counter restart) → delta<0.
		{Name: "requests_total", Step: 50, Max: 0, SpikeMult: 30,
			AutoEvery: 53, AutoBehavior: BehaviorReset},

		// Occasionally spikes (burst) → large positive delta.
		{Name: "errors_total", Step: 2, Max: 0, SpikeMult: 50,
			AutoEvery: 41, AutoBehavior: BehaviorSpike},

		// Periodically goes flat then resumes (delta==0 stretch).
		{Name: "energy_wh", Step: 12, Max: 0, SpikeMult: 10,
			AutoEvery: 17, AutoBehavior: BehaviorFlat},

		// Periodically drops a value (null gap) — must NOT read as a reset.
		{Name: "frames_total", Step: 120, Max: 0, SpikeMult: 10,
			AutoEvery: 23, AutoBehavior: BehaviorGap},
	}
}
