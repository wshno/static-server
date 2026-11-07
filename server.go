package main

import (
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
)

type mountFS struct {
	prefix string // with the trailing slash, e.g. /data/
	fsys   fs.FS
}

type server struct {
	cfg    Config
	root   fs.FS
	mounts []mountFS
	roots  []*os.Root
}

// newServer opens Root and every mount with os.OpenRoot, so neither ".." nor a symlink can reach a
// file outside them.
func newServer(cfg Config) (*server, error) {
	root, err := os.OpenRoot(cfg.Root)
	if err != nil {
		return nil, err
	}
	s := &server{cfg: cfg, root: root.FS(), roots: []*os.Root{root}}
	for _, m := range cfg.Mounts {
		r, err := os.OpenRoot(m.Dir)
		if err != nil {
			s.Close()
			return nil, err
		}
		s.roots = append(s.roots, r)
		s.mounts = append(s.mounts, mountFS{prefix: m.Prefix, fsys: r.FS()})
	}
	return s, nil
}

// Close releases the directory handles.
func (s *server) Close() {
	for _, r := range s.roots {
		r.Close()
	}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	for k, v := range s.cfg.Headers {
		h.Set(k, v)
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		http.Error(w, "405 method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// path.Clean collapses every "..", so upath stays inside the served tree; os.Root enforces it
	// again on the filesystem side.
	upath := path.Clean("/" + r.URL.Path)
	dirRequest := strings.HasSuffix(r.URL.Path, "/")
	fsys, name, mounted := s.lookup(upath)
	if hidden(upath) {
		s.notFound(w, r, mounted)
		return
	}

	st, err := fs.Stat(fsys, name)
	if err != nil {
		s.notFound(w, r, mounted)
		return
	}
	matchPath := upath
	if st.IsDir() {
		if !dirRequest {
			// Relative, like http.FileServer, so it still works behind a path-stripping proxy.
			target := path.Base(upath) + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			h.Set("Location", target)
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		// No directory listings: a directory is its index.html or nothing.
		name = path.Join(name, "index.html")
		if st, err = fs.Stat(fsys, name); err != nil || st.IsDir() {
			s.notFound(w, r, mounted)
			return
		}
		if upath != "/" {
			matchPath += "/"
		}
	} else if dirRequest {
		s.notFound(w, r, mounted)
		return
	}

	e := s.cfg.resolve(matchPath)
	if e.cacheControl != "" {
		h.Set("Cache-Control", e.cacheControl)
	}
	for k, v := range e.headers {
		h.Set(k, v)
	}
	if e.contentEncoding != "" {
		h.Set("Content-Encoding", e.contentEncoding)
	}
	if e.compress {
		h.Add("Vary", "Accept-Encoding")
		if acceptsGzip(r) {
			h.Set("Content-Encoding", "gzip")
			// Ranges would address the uncompressed bytes.
			r.Header.Del("Range")
			gw := &gzipWriter{ResponseWriter: w}
			defer gw.Close()
			w = gw
		}
	}
	s.serveFile(w, r, fsys, name, st)
}

// lookup maps a cleaned URL path to the filesystem that serves it and the fs.FS name within it.
func (s *server) lookup(upath string) (fsys fs.FS, name string, mounted bool) {
	for _, m := range s.mounts {
		base := strings.TrimSuffix(m.prefix, "/")
		if upath == base || strings.HasPrefix(upath, m.prefix) {
			return m.fsys, fsName(strings.TrimPrefix(upath, base)), true
		}
	}
	return s.root, fsName(upath), false
}

func fsName(upath string) string {
	if name := strings.TrimPrefix(upath, "/"); name != "" {
		return name
	}
	return "."
}

// hidden reports dotfiles and dot-directories (.git, .env, ...), except /.well-known/.
func hidden(upath string) bool {
	for seg := range strings.SplitSeq(upath, "/") {
		if strings.HasPrefix(seg, ".") && seg != ".well-known" {
			return true
		}
	}
	return false
}

func (s *server) notFound(w http.ResponseWriter, r *http.Request, mounted bool) {
	w.Header().Set("Cache-Control", "no-cache")
	if s.cfg.SPAFallback && !mounted {
		if st, err := fs.Stat(s.root, "index.html"); err == nil && !st.IsDir() {
			s.serveFile(w, r, s.root, "index.html", st)
			return
		}
	}
	http.Error(w, "404 page not found", http.StatusNotFound)
}

func (s *server) serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string, st fs.FileInfo) {
	f, err := fsys.Open(name)
	if err != nil {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "500 internal server error", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, path.Base(name), st.ModTime(), rs)
}

func acceptsGzip(r *http.Request) bool {
	for part := range strings.SplitSeq(r.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(part, ";")
		coding = strings.TrimSpace(coding)
		if !strings.EqualFold(coding, "gzip") && coding != "*" {
			continue
		}
		if q, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if v, err := strconv.ParseFloat(q, 64); err == nil && v == 0 {
				return false
			}
		}
		return true
	}
	return false
}

// gzipWriter compresses the body. The gzip stream starts on the first Write, so a 304 or a HEAD
// response stays empty.
type gzipWriter struct {
	http.ResponseWriter
	gz          *gzip.Writer
	wroteHeader bool
}

func (w *gzipWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		// http.ServeContent set it from the uncompressed size.
		w.Header().Del("Content-Length")
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *gzipWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.gz == nil {
		w.gz, _ = gzip.NewWriterLevel(w.ResponseWriter, gzip.BestSpeed)
	}
	return w.gz.Write(b)
}

func (w *gzipWriter) Close() error {
	if w.gz == nil {
		return nil
	}
	return w.gz.Close()
}
