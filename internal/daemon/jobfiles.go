package daemon

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"godl/internal/store"
)

// maxJobFiles caps a job_files answer. A torrent can hold tens of
// thousands of files; nobody reads past a few thousand in a list.
const maxJobFiles = 5000

// jobFileList lists what a job is made of, with how far along each
// file is. It's read only when someone opens a job's details, never
// from a download's own loop, so none of it costs a running transfer
// anything.
//
// A running torrent answers from the torrent client (exact per-file
// progress, including the files --files left out). A running WebDAV
// job answers from the remote listing it walked, with progress read
// off each local file's size. Anything else — finished, paused,
// failed — is listed from what's on disk, which for an unfinished job
// is only part of the picture; note says so.
func (d *Daemon) jobFileList(id string) (files []TorrentFile, note string, err error) {
	job, err := d.st.GetJob(context.Background(), id)
	if err != nil {
		return nil, "", fmt.Errorf("job %s not found", id)
	}

	if job.Type == store.JobTorrent && d.tm != nil {
		if infos, selected := d.tm.Files(id); infos != nil {
			for i, f := range infos {
				files = append(files, TorrentFile{Index: f.Index, Path: f.Path, Length: f.Length, Done: f.Done,
					Skipped: i < len(selected) && !selected[i]})
			}
			return capFiles(files, note)
		}
	}

	if job.Type == store.JobWebDAV {
		if rt := d.getRuntime(id); rt != nil {
			rt.mu.Lock()
			remote, root, rootIsDir := rt.webdavFiles, rt.webdavRoot, rt.webdavRootIsDir
			rt.mu.Unlock()
			if remote != nil {
				for i, f := range remote {
					tf := TorrentFile{Index: i, Path: webdavDisplayPath(root, f.Path, rootIsDir), Length: f.Size}
					if fi, err := os.Stat(webdavLocalPath(job.Output, root, f.Path, rootIsDir)); err == nil {
						tf.Done = fi.Size()
						if tf.Length > 0 && tf.Done > tf.Length {
							tf.Done = tf.Length
						}
					}
					files = append(files, tf)
				}
				return capFiles(files, note)
			}
		}
	}

	files = filesOnDisk(job)
	switch {
	case job.Status == store.StatusCompleted || job.Status == store.StatusSeeding:
	case job.Type == store.JobTorrent && len(files) > 0:
		note = "Showing what's on disk so far. Resume the torrent to see every file and its progress."
	case job.Type == store.JobTorrent:
		note = "The file list arrives once the torrent is running and has its metadata."
	case job.Type == store.JobWebDAV && len(files) > 0:
		note = "Showing what's downloaded so far. Resume the job to see every file on the server."
	case len(files) == 0:
		note = "Nothing has been saved yet."
	}
	return capFiles(files, note)
}

func capFiles(files []TorrentFile, note string) ([]TorrentFile, string, error) {
	if len(files) > maxJobFiles {
		extra := fmt.Sprintf("Showing the first %d of %d files.", maxJobFiles, len(files))
		if note != "" {
			note += " "
		}
		note += extra
		files = files[:maxJobFiles]
	}
	if files == nil {
		files = []TorrentFile{}
	}
	return files, note, nil
}

// webdavDisplayPath is a remote file's path relative to what the job
// downloads — the folder's own name and below, or just the file name.
func webdavDisplayPath(root, filePath string, rootIsDir bool) string {
	if !rootIsDir {
		return path.Base(filePath)
	}
	trimmed := strings.TrimSuffix(root, "/")
	rel := strings.TrimPrefix(strings.TrimPrefix(filePath, trimmed), "/")
	if name := path.Base(trimmed); name != "" && name != "/" && name != "." {
		return name + "/" + rel
	}
	return rel
}

// filesOnDisk lists a job's files as they are on disk now. A file the
// job is known to have finished counts as done to its size; any other
// (a paused download's half-written file) has Length 0, "size unknown",
// so it's shown as what's arrived so far rather than as complete.
func filesOnDisk(job *store.Job) []TorrentFile {
	finished := job.Status == store.StatusCompleted || job.Status == store.StatusSeeding
	complete := map[string]bool{}
	for _, p := range job.ResolvedPaths {
		complete[p] = true
	}
	var roots []string
	switch job.Type {
	case store.JobURL:
		roots = []string{job.Output}
	case store.JobTorrent:
		for _, p := range job.ResolvedPaths {
			roots = append(roots, filepath.Join(job.Output, p))
		}
	case store.JobWebDAV:
		roots = job.ResolvedPaths
		if job.Status != store.StatusCompleted {
			// An unfinished folder job only lists files it completed;
			// the folder itself also holds the ones in progress.
			if _, remote, ok := SplitWebDAVSource(job.Source); ok {
				if name := path.Base(strings.TrimSuffix(remote, "/")); name != "" && name != "/" {
					roots = []string{filepath.Join(job.Output, name)}
				}
			}
		}
	default:
		roots = job.ResolvedPaths
	}

	seen := map[string]bool{}
	var files []TorrentFile
	for _, root := range roots {
		filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() || seen[p] || strings.HasSuffix(p, ".godl-progress.json") {
				return nil
			}
			fi, err := e.Info()
			if err != nil {
				return nil
			}
			seen[p] = true
			rel := filepath.Base(p)
			if job.Type != store.JobURL {
				if r, err := filepath.Rel(job.Output, p); err == nil && !strings.HasPrefix(r, "..") {
					rel = r
				}
			}
			tf := TorrentFile{Path: filepath.ToSlash(rel), Done: fi.Size()}
			if finished || (job.Type == store.JobWebDAV && complete[p]) {
				tf.Length = fi.Size()
			}
			files = append(files, tf)
			return nil
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for i := range files {
		files[i].Index = i
	}
	return files
}
