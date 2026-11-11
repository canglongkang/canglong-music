package main

// 兼容引擎（compat server）：按苍龙播放器 core/fetchsong.py 的契约提供
// search / download 两个接口，替代上游的 SPA web 模式（安卓端用不到 SPA）。
//
// 契约（与桌面 music-dl.exe 实测样本一致，见 android/docs/P5-验证记录.md）：
//   GET  /music/        → 200（存活探测）
//   GET  /music/search?q=..&sources=a,b（sources 可重复）→ HTML 页，
//        内含 <li class="song-card" data-id=.. data-source=.. data-extra='{json}'>
//   POST /music/download?id=&source=&name=&artist=&album=&cover=&extra=&save_local=1
//        → {"saved":true,"filename":"..","path":"data/downloads/.."}
//        失败 → {"error":".."} / 已存在 → {"skipped":true,"message":".."}
//
// extra JSON 的键与 music-lib 各源一致（kugou: hash/sq_hash/hq_hash/res_hash/...），
// fetchsong.parse_cards 用它判音质档，下载时原样回传作为取链参数。

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/guohuiyuan/go-music-dl/core"
	"github.com/guohuiyuan/music-lib/model"
)

// 安卓没有 /etc/resolv.conf，Go 纯解析器会回落 127.0.0.1 导致全部 DNS 失败
// （症状：引擎源搜索 0 条且瞬间返回）。这里直接把解析请求拨到公共 DNS。
var compatDNSServers = []string{"223.5.5.5:53", "119.29.29.29:53"}

func initCompatResolver() {
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			var last error
			for _, dns := range compatDNSServers {
				d := net.Dialer{Timeout: 4 * time.Second}
				conn, err := d.DialContext(ctx, network, dns)
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		},
	}
}

var defaultCompatSources = []string{"kugou", "qq", "netease", "kuwo", "migu", "qianqian", "soda"}

func runCompatServer(port string) error {
	initCompatResolver()
	core.CM.Load()
	mux := http.NewServeMux()
	mux.HandleFunc("/music/search", handleCompatSearch)
	mux.HandleFunc("/music/download", handleCompatDownload)
	// /music/ 前缀兜底（fetchsong._ping 探测的是 /music/，必须 200）
	mux.HandleFunc("/music/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!DOCTYPE html><html><body>music-dl compat</body></html>")
	})
	srv := &http.Server{
		Addr:         "127.0.0.1:" + port,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
	}
	return srv.ListenAndServe()
}

// ---------- search ----------

func compatSourceList(r *http.Request) []string {
	raw := r.URL.Query()["sources"]
	var out []string
	seen := map[string]bool{}
	for _, v := range raw {
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return defaultCompatSources
	}
	return out
}

func handleCompatSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "missing q", http.StatusBadRequest)
		return
	}
	sources := compatSourceList(r)

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		songs   []model.Song
		deadline = time.Now().Add(35 * time.Second)
	)
	for _, src := range sources {
		search := core.GetSearchFunc(src)
		if search == nil {
			continue
		}
		wg.Add(1)
		go func(src string, search core.SearchFunc) {
			defer wg.Done()
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			done := make(chan result, 1)
			go func() {
				ss, err := search(q)
				done <- result{ss, err}
			}()
			select {
			case res := <-done:
				if res.err != nil || len(res.songs) == 0 {
					return
				}
				mu.Lock()
				songs = append(songs, res.songs...)
				mu.Unlock()
			case <-ctx.Done():
			}
		}(src, search)
	}
	wg.Wait()

	sort.SliceStable(songs, func(i, j int) bool {
		if songs[i].Source != songs[j].Source {
			return songs[i].Source < songs[j].Source
		}
		return songs[i].Name < songs[j].Name
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html><head><meta charset=\"UTF-8\"></head><body><ul class=\"song-list\">\n")
	for i := range songs {
		writeCompatCard(&b, &songs[i])
	}
	b.WriteString("</ul></body></html>")
	fmt.Fprint(w, b.String())
}

type result struct {
	songs []model.Song
	err   error
}

func writeCompatCard(b *strings.Builder, s *model.Song) {
	if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Source) == "" {
		return
	}
	extra := "{}"
	if s.Extra != nil {
		if raw, err := json.Marshal(s.Extra); err == nil {
			extra = string(raw)
		}
	}
	fmt.Fprintf(b, "<li class=\"song-card\"\n"+
		"            data-id=%q\n"+
		"            data-source=%q\n"+
		"            data-album-id=%q\n"+
		"            data-album=%q\n"+
		"            data-duration=%q\n"+
		"            data-name=%q\n"+
		"            data-artist=%q\n"+
		"            data-cover=%q\n"+
		"            data-sort-size=\"\"\n"+
		"            data-sort-bitrate=\"\"\n"+
		"            data-extra='%s'>\n"+
		"</li>\n",
		html.EscapeString(s.ID),
		html.EscapeString(s.Source),
		html.EscapeString(s.AlbumID),
		html.EscapeString(s.Album),
		html.EscapeString(strconv.Itoa(s.Duration)),
		html.EscapeString(s.Name),
		html.EscapeString(s.Artist),
		html.EscapeString(s.Cover),
		html.EscapeString(extra))
}

// ---------- download ----------

func handleCompatDownload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	song := &model.Song{
		ID:     strings.TrimSpace(q.Get("id")),
		Source: strings.TrimSpace(q.Get("source")),
		Name:   strings.TrimSpace(q.Get("name")),
		Artist: strings.TrimSpace(q.Get("artist")),
		Album:  strings.TrimSpace(q.Get("album")),
		Cover:  strings.TrimSpace(q.Get("cover")),
	}
	if song.ID == "" || song.Source == "" {
		http.Error(w, "Missing params", http.StatusBadRequest)
		return
	}
	if raw := strings.TrimSpace(q.Get("extra")); raw != "" {
		m := map[string]string{}
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			song.Extra = m
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	res, err := core.SaveSongToFileWithoutLyricsSidecar(song, "data/downloads", false, false, "")
	if err != nil {
		if res != nil && res.Skipped {
			fmt.Fprint(w, `{"skipped":true,"message":"已存在"}`)
			return
		}
		msg := fmt.Sprintf("%v", err)
		if len(msg) > 120 {
			msg = msg[:120]
		}
		fmt.Fprintf(w, `{"error":%q}`, msg)
		return
	}
	// 相对路径（cwd = 引擎工作目录），fetchsong 会与 work_dir 拼接
	rel := "data/downloads/" + strings.ReplaceAll(res.Filename, "\\", "/")
	fmt.Fprintf(w, `{"saved":true,"filename":%q,"path":%q,"ext":%q}`, res.Filename, rel, res.Ext)
}
