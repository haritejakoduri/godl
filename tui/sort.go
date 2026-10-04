package tui

import (
	"path"
	"path/filepath"
	"sort"
	"strings"

	"godl/internal/daemon"
	"godl/internal/store"
	"godl/internal/webdav"
)

// jobSort is what the dashboard orders jobs by. "t" steps through
// them; "T" reverses whichever is chosen. Each starts in the direction
// you'd usually want (biggest, fastest, furthest along first).
type jobSort int

const (
	sortNewest jobSort = iota
	sortName
	sortStatus
	sortProgress
	sortSize
	sortSpeed
	sortTimeLeft
	jobSortCount
)

var jobSortNames = [...]string{"newest", "name", "status", "progress", "size", "speed", "time left"}

func (s jobSort) String() string { return jobSortNames[s] }

// statusRank orders the Status sort: what needs attention or is moving
// first, finished last.
var statusRank = map[store.JobStatus]int{
	store.StatusActive: 0, store.StatusQueued: 1, store.StatusSeeding: 2,
	store.StatusPaused: 3, store.StatusFailed: 4, store.StatusCanceled: 5, store.StatusCompleted: 6,
}

// jobName is what a job is called for sorting: the file or folder it
// saves to, or its source when that says more (a WebDAV path, a magnet).
func jobName(j *daemon.JobView) string {
	if j.Type == store.JobWebDAV {
		if _, remote, ok := daemon.SplitWebDAVSource(j.Source); ok {
			return path.Base(strings.TrimSuffix(remote, "/"))
		}
	}
	if j.Type == store.JobURL && j.Output != "" {
		return filepath.Base(j.Output)
	}
	if len(j.ResolvedPaths) > 0 {
		return filepath.Base(j.ResolvedPaths[0])
	}
	return j.Source
}

func jobFraction(j *daemon.JobView) float64 {
	if j.BytesTotal <= 0 {
		if j.Status == store.StatusCompleted {
			return 1
		}
		return 0
	}
	return float64(j.BytesDone) / float64(j.BytesTotal)
}

// sortJobs returns jobs (store order, oldest first) in the chosen
// order. Ties keep newest first, so equal rows don't shuffle between
// snapshots.
func sortJobs(jobs []*daemon.JobView, by jobSort, reverse bool) []*daemon.JobView {
	out := newestFirst(jobs)
	if by == sortNewest {
		if reverse {
			return append([]*daemon.JobView(nil), jobs...)
		}
		return out
	}
	less := func(a, b *daemon.JobView) bool {
		switch by {
		case sortName:
			return strings.ToLower(jobName(a)) < strings.ToLower(jobName(b))
		case sortStatus:
			return statusRank[a.Status] < statusRank[b.Status]
		case sortProgress:
			return jobFraction(a) > jobFraction(b)
		case sortSize:
			return a.BytesTotal > b.BytesTotal
		case sortSpeed:
			return a.SpeedBps > b.SpeedBps
		case sortTimeLeft:
			// Unknown (no estimate) sorts after every known one.
			ea, eb := a.ETASeconds, b.ETASeconds
			if (ea < 0) != (eb < 0) {
				return ea >= 0
			}
			return ea < eb
		}
		return false
	}
	sort.SliceStable(out, func(i, j int) bool {
		if reverse {
			return less(out[j], out[i])
		}
		return less(out[i], out[j])
	})
	return out
}

// entrySort is what the WebDAV browser orders a folder by. Folders are
// always listed before files, whatever the order.
type entrySort int

const (
	entryByName entrySort = iota
	entryBySize
	entryByModified
	entrySortCount
)

var entrySortNames = [...]string{"name", "size", "modified"}

func (s entrySort) String() string { return entrySortNames[s] }

func sortEntries(entries []webdav.Entry, by entrySort, reverse bool) []webdav.Entry {
	out := append([]webdav.Entry(nil), entries...)
	less := func(a, b webdav.Entry) bool {
		switch by {
		case entryBySize:
			if a.Size != b.Size {
				return a.Size > b.Size
			}
		case entryByModified:
			if !a.ModTime.Equal(b.ModTime) {
				return a.ModTime.After(b.ModTime)
			}
		}
		return strings.ToLower(a.Path) < strings.ToLower(b.Path)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		if reverse {
			return less(out[j], out[i])
		}
		return less(out[i], out[j])
	})
	return out
}
