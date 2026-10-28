package taskapi

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UploadsVersion is the version of uploads (PUT /v1/uploads/{name},
// PostRequest.Upload) this server serves, 0 when it does not.
const UploadsVersion = 1

// maxUploadBytes bounds one upload's files: a week of a repository's
// archived agent traces, for the retro, is well under it.
const maxUploadBytes = 4 << 30

// staleUpload is how long an upload no task took is kept.
const staleUpload = time.Hour

// uploadsDir is where uploads wait for the task that takes them: beside
// the tasks, not among them, where a listing would take one for a task.
func (s *Server) uploadsDir() string {
	return filepath.Join(filepath.Dir(s.tasksDir), "uploads")
}

// handleUpload unpacks a tar of files to give a task (PostRequest.Upload),
// replacing an upload of the same name: a client's retry.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	upload, ok := pathName(w, r, "name")
	if !ok {
		return
	}
	s.pruneUploads()
	dir := filepath.Join(s.uploadsDir(), upload)
	tmp := dir + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := untar(tmp, http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20), maxUploadBytes); err != nil {
		_ = os.RemoveAll(tmp)
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unpacking upload %s: %v", upload, err))
		return
	}
	_ = os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.RemoveAll(tmp)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// takeUpload moves an upload into the task's directory at to, a relative
// path in it.
func (s *Server) takeUpload(taskDir, upload, to string) error {
	if !name.MatchString(upload) {
		return fmt.Errorf("invalid upload %q", upload)
	}
	dest, err := under(taskDir, to)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(s.uploadsDir(), upload), dest); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no upload %s: upload the files before the task", upload)
		}
		return err
	}
	return nil
}

// pruneUploads removes the uploads no task took within staleUpload.
func (s *Server) pruneUploads() {
	entries, err := os.ReadDir(s.uploadsDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleUpload {
			_ = os.RemoveAll(filepath.Join(s.uploadsDir(), e.Name()))
		}
	}
}

// untar unpacks a tar's directories and regular files under dir, at most
// limit bytes of files, refusing any other entry or a path that leaves dir.
func untar(dir string, r io.Reader, limit int64) error {
	tr := tar.NewReader(r)
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		dest, err := under(dir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if total += hdr.Size; total > limit {
				return fmt.Errorf("over %d bytes", limit)
			}
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, io.LimitReader(tr, hdr.Size))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: only directories and regular files", hdr.Name)
		}
	}
}

// under is rel inside dir, refused if it would leave it.
func under(dir, rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe path %q", rel)
	}
	return filepath.Join(dir, clean), nil
}

// tarDir writes dir's directories and regular files to w as a tar, named
// relative to dir.
func tarDir(w io.Writer, dir string) error {
	tw := tar.NewWriter(w)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil // a link, a socket: not a file to give a task
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	return tw.Close()
}
