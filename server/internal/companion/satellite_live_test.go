package companion

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Satellite wire format is Companion's, not ours, so the parser is pinned to
// frames captured from live instances rather than to hand-written lines that can
// drift toward whatever the parser happens to accept. testdata holds real
// server->surface frames from Companion 3.4.1 (the deployment target) and 5.0.3
// (current release), which differ in ways worth keeping under test: 5.x quotes
// its values and sends PRESSED=0/1 plus a LOCATION field, where 3.4.x leaves
// values bare and sends PRESSED=false/true.
//
// bitmapBytes is 72*72*3: Companion renders a square button at the BITMAPS size
// we advertise, 3 bytes per pixel, no alpha and no row padding. The client's
// RGB->RGBA conversion depends on exactly that, so a change here should fail.
const bitmapBytes = 72 * 72 * 3

func loadFixture(t *testing.T, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var lines []string
	for _, ln := range strings.Split(string(raw), "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		lines = append(lines, ln)
	}
	if len(lines) == 0 {
		t.Fatalf("fixture %s has no frames", name)
	}
	return lines
}

func TestParseLiveCompanionFrames(t *testing.T) {
	for _, fixture := range []string{"keystate-3.4.1.txt", "keystate-5.0.3.txt"} {
		t.Run(fixture, func(t *testing.T) {
			var keyStates int
			for _, line := range loadFixture(t, fixture) {
				cmd, args := parseSatelliteLine(line)
				if cmd == "" {
					t.Fatalf("no command parsed from %.60q", line)
				}
				if cmd != "KEY-STATE" {
					continue
				}
				keyStates++

				// Quoting differs between versions; the parser must strip it so
				// downstream code never sees a stray quote in a value.
				for _, k := range []string{"DEVICEID", "TYPE"} {
					if v := args[k]; strings.ContainsAny(v, `"`) {
						t.Errorf("%s still quoted: %q", k, v)
					}
				}
				if args["DEVICEID"] == "" {
					t.Error("DEVICEID missing")
				}
				switch typ := args["TYPE"]; typ {
				case "BUTTON", "PAGEUP", "PAGEDOWN", "PAGENUM":
					// The four types live Companion actually emits for a
					// simple-mode surface; the client renders a glyph per type
					// when a bitmap is absent.
				default:
					t.Errorf("unexpected TYPE %q", typ)
				}
				if _, ok := args["BITMAP"]; !ok {
					t.Fatal("KEY-STATE carried no BITMAP")
				}
				decoded, err := base64.StdEncoding.DecodeString(args["BITMAP"])
				if err != nil {
					t.Fatalf("BITMAP is not valid base64: %v", err)
				}
				if len(decoded) != bitmapBytes {
					t.Errorf("bitmap is %d bytes, want %d (72x72 RGB)", len(decoded), bitmapBytes)
				}
				// Both versions send a COLOR when COLORS=hex is requested.
				if c := args["COLOR"]; !strings.HasPrefix(c, "#") {
					t.Errorf("COLOR = %q, want a #rrggbb value", c)
				}
				// PRESSED is false/true on 3.4.x and 0/1 on 5.x; both must read false here.
				if parseWireBool(args["PRESSED"]) {
					t.Errorf("PRESSED = %q parsed as true, want false", args["PRESSED"])
				}
			}
			if keyStates == 0 {
				t.Fatal("fixture contained no KEY-STATE frames")
			}
		})
	}
}

// TestLiveHandshakeLines covers the non-KEY-STATE frames a real Companion sends
// before and alongside the surface, all of which handleLine must tolerate: 5.x
// adds CAPS (advertising bitmap formats) and quotes its values.
func TestLiveHandshakeLines(t *testing.T) {
	cases := []struct {
		line    string
		wantCmd string
		check   func(*testing.T, map[string]string)
	}{
		{
			line:    `BEGIN CompanionVersion=3.4.1+7323-stable-e32a1052 ApiVersion=1.7.0`,
			wantCmd: "BEGIN",
			check: func(t *testing.T, a map[string]string) {
				if !strings.HasPrefix(a["CompanionVersion"], "3.4.1") {
					t.Errorf("CompanionVersion = %q", a["CompanionVersion"])
				}
			},
		},
		{
			line:    `BEGIN CompanionVersion="5.0.3+9703-stable-2daa0d7670" ApiVersion="1.12.0" `,
			wantCmd: "BEGIN",
			check: func(t *testing.T, a map[string]string) {
				if !strings.HasPrefix(a["CompanionVersion"], "5.0.3") {
					t.Errorf("CompanionVersion = %q (quotes not stripped?)", a["CompanionVersion"])
				}
			},
		},
		{
			line:    `CAPS SUBSCRIPTIONS=0 NONSQUARE=1 BITMAP_FORMATS="rgb,png,webp" `,
			wantCmd: "CAPS",
			check: func(t *testing.T, a map[string]string) {
				if a["BITMAP_FORMATS"] != "rgb,png,webp" {
					t.Errorf("BITMAP_FORMATS = %q", a["BITMAP_FORMATS"])
				}
			},
		},
		{
			line:    `ADD-DEVICE OK DEVICEID="cuebooth"`,
			wantCmd: "ADD-DEVICE",
			check: func(t *testing.T, a map[string]string) {
				if a["DEVICEID"] != "cuebooth" {
					t.Errorf("DEVICEID = %q", a["DEVICEID"])
				}
				if _, ok := a["OK"]; !ok {
					t.Error("bare OK token not captured")
				}
			},
		},
		{
			line:    `BRIGHTNESS DEVICEID="cuebooth" VALUE=100`,
			wantCmd: "BRIGHTNESS",
			check:   func(*testing.T, map[string]string) {},
		},
		{
			line:    `KEY-PRESS OK`,
			wantCmd: "KEY-PRESS",
			check:   func(*testing.T, map[string]string) {},
		},
	}
	for _, tc := range cases {
		cmd, args := parseSatelliteLine(tc.line)
		if cmd != tc.wantCmd {
			t.Errorf("line %.40q: cmd = %q, want %q", tc.line, cmd, tc.wantCmd)
			continue
		}
		tc.check(t, args)
	}
}

// TestLivePressedEncodings pins the two PRESSED spellings observed across
// versions, so parseWireBool can't be narrowed to just one of them.
func TestLivePressedEncodings(t *testing.T) {
	for _, s := range []string{"true", "1"} {
		if !parseWireBool(s) {
			t.Errorf("parseWireBool(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"false", "0", ""} {
		if parseWireBool(s) {
			t.Errorf("parseWireBool(%q) = true, want false", s)
		}
	}
}
