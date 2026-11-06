package main

// `leapfrog live <db>`: a small web page on localhost that shows every key and value of a database and updates
// by itself, so a judge can watch the data change while operations run and while HYDRA kills the database.
//
// The viewer never opens the database through the engine (that would take its lock and could get in the way of
// the writer or of a kill test). It reads the file directly, read-only, with the same independent decoder that
// `leapfrog inspect` uses: superblock, then frames in order, stopping at the first invalid one exactly as
// recovery does.

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

type kv struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Size  int    `json:"size"`
}

type liveSnapshot struct {
	DB      string `json:"db"`
	Time    string `json:"time"`
	Exists  bool   `json:"exists"`
	Error   string `json:"error,omitempty"`
	Keys    int    `json:"keys"`
	Bytes   int64  `json:"bytes"`
	Frames  int    `json:"frames"`
	Tail    string `json:"tail"`
	Version uint64 `json:"version"`
	Items   []kv   `json:"items"`
}

// decodeOps walks the operations of one transaction frame payload: count, then kind, key length, value length,
// key, value for each. kind 1 is put, 2 is delete.
func decodeOps(p []byte, apply func(kind byte, key, val []byte)) bool {
	if len(p) < 4 {
		return false
	}
	le := binary.LittleEndian
	n, off := int(le.Uint32(p[0:4])), 4
	for i := 0; i < n; i++ {
		if len(p) < off+7 {
			return false
		}
		kind, kl, vl := p[off], int(le.Uint16(p[off+1:off+3])), int(le.Uint32(p[off+3:off+7]))
		off += 7
		if len(p) < off+kl+vl {
			return false
		}
		apply(kind, p[off:off+kl], p[off+kl:off+kl+vl])
		off += kl + vl
	}
	return true
}

const liveValueCap = 4096 // bytes of one value shown on the page; the real size is always shown

// snapshotDB reads the current committed state of the database file, read-only.
func snapshotDB(path string) liveSnapshot {
	snap := liveSnapshot{DB: path, Time: time.Now().Format("15:04:05.000"), Items: []kv{}}
	st, err := os.Stat(path)
	if err != nil {
		snap.Error = "file not found yet: it appears when the first operation creates it"
		return snap
	}
	snap.Exists, snap.Bytes = true, st.Size()
	im, err := readImage(path)
	if err != nil {
		snap.Error = err.Error()
		return snap
	}
	f, err := os.Open(path)
	if err != nil {
		snap.Error = err.Error()
		return snap
	}
	defer f.Close()
	state := map[string][]byte{}
	for _, fr := range im.frames {
		snap.Version = fr.seq
		if fr.typ != 1 || fr.shortMiss > 0 {
			continue
		}
		p := make([]byte, fr.payload)
		if _, err := f.ReadAt(p, fr.off+hdrLen); err != nil && err != io.EOF {
			continue
		}
		decodeOps(p, func(kind byte, key, val []byte) {
			if kind == 1 {
				state[string(key)] = append([]byte(nil), val...)
			} else if kind == 2 {
				delete(state, string(key))
			}
		})
	}
	snap.Frames, snap.Tail = len(im.frames), im.tail
	keys := make([]string, 0, len(state))
	for k := range state {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := state[k]
		shown := v
		if len(shown) > liveValueCap {
			shown = shown[:liveValueCap]
		}
		snap.Items = append(snap.Items, kv{Key: esc([]byte(k)), Value: esc(shown), Size: len(v)})
	}
	snap.Keys = len(keys)
	return snap
}

func cmdLive(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("live", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	port := fs.Int("port", 8099, "")
	quiet := fs.Bool("quiet", false, "")
	path, flagArgs := splitPath(args)
	if err := fs.Parse(flagArgs); err != nil || path == "" {
		fmt.Fprintln(out, "usage: leapfrog live <db> [--port 8099]")
		return 2
	}
	abs, _ := filepath.Abs(path)
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(*port))
	if err != nil {
		fmt.Fprintf(out, "cannot listen on port %d: %v\n(try another port: leapfrog live %s --port 8100)\n", *port, err, path)
		return 1
	}
	var mu sync.Mutex
	srv := &http.Server{Handler: liveHandler(abs, &mu)}
	if !*quiet {
		fmt.Fprintf(out, "live viewer for %s on http://localhost:%d  (Ctrl-C to stop)\n", abs, *port)
	}
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(out, err)
		return 1
	}
	return 0
}

func liveHandler(abs string, mu *sync.Mutex) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ping", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"db": abs})
	})
	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		snap := snapshotDB(abs)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(snap)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, liveHTML)
	})
	return mux
}

const liveHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>LEAPFROG-R live</title><style>
:root{--bg:#0e1116;--card:#161b22;--line:#2a313c;--fg:#e6edf3;--dim:#8b949e;--ok:#3fb950;--new:#2ea043;--chg:#d29922;--del:#f85149;--acc:#39c5cf}
@media(prefers-color-scheme:light){:root{--bg:#f6f8fa;--card:#fff;--line:#d0d7de;--fg:#1f2328;--dim:#59636e;--ok:#1a7f37;--new:#dafbe1;--chg:#fff8c5;--del:#ffebe9;--acc:#0969da}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.4 system-ui,sans-serif}
header{padding:16px;border-bottom:1px solid var(--line);display:flex;flex-wrap:wrap;gap:8px 24px;align-items:baseline}
h1{margin:0;font-size:20px;color:var(--acc)}.m{color:var(--dim)}main{padding:16px;max-width:1100px;margin:auto}
.stats{display:flex;flex-wrap:wrap;gap:10px;margin-bottom:14px}.s{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:8px 14px}
.s b{display:block;font-size:20px}table{width:100%;border-collapse:collapse;background:var(--card);border:1px solid var(--line);border-radius:8px;overflow:hidden}
th,td{padding:7px 12px;text-align:left;border-bottom:1px solid var(--line);vertical-align:top}th{color:var(--dim);font-weight:600}
td.k,td.v{font-family:ui-monospace,Menlo,monospace;word-break:break-all}.sz{color:var(--dim);white-space:nowrap}
tr.new{animation:f 3s}tr.chg{animation:g 3s}@keyframes f{from{background:var(--new)}}@keyframes g{from{background:var(--chg)}}
#log{margin-top:14px;background:var(--card);border:1px solid var(--line);border-radius:8px;padding:10px 14px;font-family:ui-monospace,Menlo,monospace;font-size:13px;max-height:220px;overflow:auto}
.add{color:var(--ok)}.upd{color:var(--chg)}.rm{color:var(--del)}.err{color:var(--del);padding:10px 0}
</style></head><body>
<header><h1>LEAPFROG-R · live data</h1><span class="m" id="db"></span><span class="m" id="up"></span></header>
<main><div class="stats"><div class="s"><span class="m">keys</span><b id="keys">0</b></div><div class="s"><span class="m">file size</span><b id="bytes">0</b></div>
<div class="s"><span class="m">frames</span><b id="frames">0</b></div><div class="s"><span class="m">file tail</span><b id="tail">-</b></div></div>
<div class="err" id="err"></div>
<table><thead><tr><th>key</th><th>value</th><th>size</th></tr></thead><tbody id="rows"></tbody></table>
<div id="log"><span class="m">changes appear here as they happen (read straight from the file, every half second)</span></div></main>
<script>
var prev=null;
function esc(s){return String(s).replace(/[&<>]/g,function(c){return {'&':'&amp;','<':'&lt;','>':'&gt;'}[c]})}
function logLine(cls,t){var l=document.getElementById('log');if(l.firstChild&&l.firstChild.className==='m')l.innerHTML='';
 var d=document.createElement('div');d.className=cls;d.textContent=new Date().toLocaleTimeString()+'  '+t;l.insertBefore(d,l.firstChild);while(l.childNodes.length>200)l.removeChild(l.lastChild)}
function render(s){
 document.getElementById('db').textContent=s.db;document.getElementById('up').textContent='updated '+s.time;
 document.getElementById('keys').textContent=s.keys;document.getElementById('bytes').textContent=s.bytes.toLocaleString()+' B';
 document.getElementById('frames').textContent=s.frames;document.getElementById('tail').textContent=s.tail||'-';
 document.getElementById('err').textContent=s.error||'';
 var cur={};s.items.forEach(function(i){cur[i.key]=i});
 var cls={};
 if(prev){s.items.forEach(function(i){var p=prev[i.key];if(!p){cls[i.key]='new';logLine('add','+ '+i.key+' = '+i.value.slice(0,120))}
   else if(p.value!==i.value){cls[i.key]='chg';logLine('upd','~ '+i.key+' = '+i.value.slice(0,120))}});
   Object.keys(prev).forEach(function(k){if(!cur[k])logLine('rm','- '+k+' (deleted)')})}
 var h='';s.items.forEach(function(i){h+='<tr'+(cls[i.key]?' class="'+cls[i.key]+'"':'')+'><td class="k">'+esc(i.key)+'</td><td class="v">'+esc(i.value)+'</td><td class="sz">'+i.size+' B</td></tr>'});
 document.getElementById('rows').innerHTML=h||'<tr><td colspan="3" class="m">no keys</td></tr>';prev=cur}
function tick(){fetch('/api/state',{cache:'no-store'}).then(function(r){return r.json()}).then(render).catch(function(){document.getElementById('err').textContent='viewer lost: is the server still running?'})}
tick();setInterval(tick,500);
</script></body></html>`
