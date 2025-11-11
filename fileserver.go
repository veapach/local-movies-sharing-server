package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var dir string
var addr string
var speedBytes int64

func main() {
	flag.StringVar(&dir, "dir", ".", "")
	flag.StringVar(&addr, "addr", "0.0.0.0:8080", "")
	flag.Int64Var(&speedBytes, "speedbytes", 50<<20, "bytes to stream in /speedtest default 50MB")
	flag.Parse()
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		fmt.Fprintln(os.Stderr, "invalid dir")
		os.Exit(1)
	}
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/speedtest", speedTestHandler)
	server := &http.Server{Addr: addr, ReadTimeout: 0, WriteTimeout: 0, IdleTimeout: 0}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen error:", err)
		os.Exit(1)
	}
	fmt.Printf("Serving %s on http://%s\n", dir, ln.Addr().String())
	if err := server.Serve(ln); err != nil {
		fmt.Fprintln(os.Stderr, "server error:", err)
	}
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	upath := filepath.Clean(r.URL.Path)
	full := filepath.Join(dir, upath)
	fi, err := os.Stat(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if fi.IsDir() {
		f, err := os.Open(full)
		if err != nil {
			http.Error(w, "cannot open dir", http.StatusInternalServerError)
			return
		}
		list, err := f.Readdir(-1)
		_ = f.Close()
		if err != nil {
			http.Error(w, "cannot read dir", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<html><head><meta charset='utf-8'><title>%s</title></head><body><h1>%s</h1><ul>", upath, upath)
		for _, e := range list {
			name := e.Name()
			href := filepath.ToSlash(filepath.Join(upath, name))
			fmt.Fprintf(w, "<li><a href=\"%s\">%s</a> %s</li>", href, name, human(e.Size()))
		}
		fmt.Fprint(w, "</ul></body></html>")
		return
	}
	serveFileFast(w, r, full, fi)
}

func serveFileFast(w http.ResponseWriter, r *http.Request, path string, fi os.FileInfo) {
	if r.Header.Get("Range") != "" {
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, "cannot open file", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
		_ = f.Close()
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "cannot open file", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	typ := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if typ == "" {
		typ = "application/octet-stream"
	}
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	w.Header().Set("Accept-Ranges", "bytes")
	buf := make([]byte, 1<<20)
	start := time.Now()
	n, err := io.CopyBuffer(w, f, buf)
	if err != nil {
		return
	}
	elapsed := time.Since(start).Seconds()
	if elapsed == 0 {
		elapsed = 0.000001
	}
	fmt.Fprintf(os.Stdout, "%s transferred %s in %.2fs (%.2f MB/s)\n", fi.Name(), human(n), elapsed, float64(n)/(1024*1024)/elapsed)
}

func speedTestHandler(w http.ResponseWriter, r *http.Request) {
	fileParam := r.URL.Query().Get("file")
	var target string
	if fileParam != "" {
		candidate := filepath.Join(dir, filepath.Clean("/"+fileParam))
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			target = candidate
		}
	}
	if target == "" {
		found := ""
		_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if ext == ".mkv" || ext == ".mp4" || ext == ".ts" || ext == ".m2ts" || ext == ".iso" {
				found = p
				return io.EOF
			}
			return nil
		})
		if found == "" {
			http.Error(w, "no media file found for speedtest", http.StatusNotFound)
			return
		}
		target = found
	}
	f, err := os.Open(target)
	if err != nil {
		http.Error(w, "cannot open file", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	size := speedBytes
	if size <= 0 {
		size = 50 << 20
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.Header().Set("Accept-Ranges", "bytes")
	buf := make([]byte, 1<<20)
	remaining := size
	start := time.Now()
	total := int64(0)
	for remaining > 0 {
		toRead := int64(len(buf))
		if remaining < toRead {
			toRead = remaining
		}
		nr, err := f.Read(buf[:toRead])
		if nr > 0 {
			nw, errw := w.Write(buf[:nr])
			if errw != nil || nw != nr {
				break
			}
			total += int64(nw)
			remaining -= int64(nw)
		}
		if err != nil {
			break
		}
	}
	elapsed := time.Since(start).Seconds()
	if elapsed == 0 {
		elapsed = 0.000001
	}
	res := map[string]interface{}{
		"file":       filepath.Base(target),
		"bytes_sent": total,
		"mb_per_s":   float64(total) / (1024*1024) / elapsed,
		"duration_s": elapsed,
	}
	js, _ := json.Marshal(res)
	fmt.Fprintln(os.Stdout, string(js))
	w.Header().Set("Content-Type", "application/json")
	w.Write(js)
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
