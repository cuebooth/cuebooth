// Package api hosts the WebSocket API the Flutter client connects to.
//
// The server is authoritative for state; clients send commands and receive
// state broadcasts. Audio meters travel on a separate higher-frequency
// channel to avoid flooding the main state stream. See docs/design.md
// §3.6 (Communication Protocol) and docs/protocol.md for the wire spec.
package api
