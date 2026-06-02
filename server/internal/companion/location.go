package companion

import (
	"fmt"
	"strconv"
	"strings"
)

// Location identifies a single button on a Companion surface by its
// page/row/column coordinates, matching Companion's HTTP API path segments
// (/api/location/<page>/<row>/<column>/...).
//
// Page numbers are 1-based in Companion; row and column are 0-based grid
// positions. CueBooth stores these as the string "page/row/column" in the
// config file (see configs/cuebooth.example.toml) and parses them with
// ParseLocation.
type Location struct {
	Page   int
	Row    int
	Column int
}

// ParseLocation parses a "page/row/column" coordinate string (e.g. "7/0/2")
// into a Location. All three components are required and must be non-negative
// integers; the page must be at least 1.
func ParseLocation(s string) (Location, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 3 {
		return Location{}, fmt.Errorf("invalid button coordinate %q: want \"page/row/column\"", s)
	}

	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return Location{}, fmt.Errorf("invalid button coordinate %q: component %q is not an integer", s, p)
		}
		if n < 0 {
			return Location{}, fmt.Errorf("invalid button coordinate %q: component %q is negative", s, p)
		}
		nums[i] = n
	}

	loc := Location{Page: nums[0], Row: nums[1], Column: nums[2]}
	if loc.Page < 1 {
		return Location{}, fmt.Errorf("invalid button coordinate %q: page must be >= 1", s)
	}
	return loc, nil
}

// String renders the location back to its "page/row/column" form.
func (l Location) String() string {
	return fmt.Sprintf("%d/%d/%d", l.Page, l.Row, l.Column)
}
