// Package audio is the direct OSC client for the mixer (Behringer XR-series).
//
// This package bypasses Companion because Companion doesn't expose meter
// data and HTTP round-trips are too slow for continuous fader control.
// Capabilities include real-time meter streaming, fader/gain/EQ control,
// DCA group control, channel profile storage, and (later) automation
// (feedback detection, auto-leveling). See docs/design.md §3.3 (Audio Engine).
package audio
