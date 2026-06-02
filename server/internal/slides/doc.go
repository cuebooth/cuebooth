// Package slides parses the @cuebooth DSL embedded in slide notes and
// executes the resulting rules — either immediately or queued for operator
// confirmation. Slide change events are delivered to the server by the C#
// sidecar over a local IPC channel. See docs/design.md §3.4 (Slide Rule
// Format) and §3.3 (Slide Engine).
package slides
