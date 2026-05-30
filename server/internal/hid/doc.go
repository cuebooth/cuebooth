// Package hid reads raw USB HID events from the slide clicker (Norwii N29
// or compatible). Replaces the vendor remapping app + AutoHotkey script
// chain with a single in-process handler. See docs/design.md §3.3 (HID
// Input Handler).
package hid
