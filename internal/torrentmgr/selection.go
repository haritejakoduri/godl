package torrentmgr

import (
	"fmt"
	"path"
	"strconv"
	"strings"
)

// Selection is a parsed --files value: which of a torrent's files to
// download. A nil *Selection selects everything.
type Selection struct {
	items []selItem
}

type selItem struct {
	lo, hi int    // 1-based, inclusive; unused when glob is set
	glob   string // matched against the file's full path and its base name
}

// ParseSelection parses a comma-separated list of 1-based file numbers
// ("3"), ranges ("5-9") and glob patterns ("*.mkv", "Season 1/*"), as
// printed by "godl torrent --list-files". An empty spec returns nil.
func ParseSelection(spec string) (*Selection, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	s := &Selection{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, ok, err := parseRange(part); ok {
			if err != nil {
				return nil, err
			}
			s.items = append(s.items, selItem{lo: lo, hi: hi})
			continue
		}
		if _, err := path.Match(part, ""); err != nil {
			return nil, fmt.Errorf("--files: bad pattern %q: %w", part, err)
		}
		s.items = append(s.items, selItem{glob: part})
	}
	if len(s.items) == 0 {
		return nil, nil
	}
	return s, nil
}

// parseRange reports ok for anything shaped like "N" or "N-M". err is
// set for a well-shaped but invalid one, such as "0" or "9-3".
func parseRange(part string) (lo, hi int, ok bool, err error) {
	loStr, hiStr, isRange := strings.Cut(part, "-")
	lo, lerr := strconv.Atoi(strings.TrimSpace(loStr))
	if lerr != nil {
		return 0, 0, false, nil
	}
	hi = lo
	if isRange {
		h, herr := strconv.Atoi(strings.TrimSpace(hiStr))
		if herr != nil {
			return 0, 0, false, nil
		}
		hi = h
	}
	if lo < 1 || hi < lo {
		return 0, 0, true, fmt.Errorf("--files: invalid file number or range %q (files are numbered from 1)", part)
	}
	return lo, hi, true, nil
}

// Match reports whether the file numbered index (1-based) at filePath
// is selected.
func (s *Selection) Match(index int, filePath string) bool {
	if s == nil {
		return true
	}
	for _, it := range s.items {
		if it.glob == "" {
			if index >= it.lo && index <= it.hi {
				return true
			}
			continue
		}
		if ok, _ := path.Match(it.glob, filePath); ok {
			return true
		}
		if ok, _ := path.Match(it.glob, path.Base(filePath)); ok {
			return true
		}
	}
	return false
}

// SelectionSpec is the inverse of ParseSelection for a plain choice of
// files: selected[i] says whether file i+1 is wanted. It returns "" when
// every file is (the same as no selection at all), and otherwise runs
// of consecutive files as ranges — "1-40,43" rather than 41 numbers, so
// a big season pack's choice stays short.
func SelectionSpec(selected []bool) string {
	all := true
	for _, s := range selected {
		if !s {
			all = false
			break
		}
	}
	if all {
		return ""
	}
	var parts []string
	for i := 0; i < len(selected); {
		if !selected[i] {
			i++
			continue
		}
		j := i
		for j+1 < len(selected) && selected[j+1] {
			j++
		}
		if i == j {
			parts = append(parts, strconv.Itoa(i+1))
		} else {
			parts = append(parts, strconv.Itoa(i+1)+"-"+strconv.Itoa(j+1))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}
