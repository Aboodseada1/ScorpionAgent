package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"scorpion/agent/internal/audio"
	"scorpion/agent/internal/session"
)

// sessionEntry ties a session to its transport fan-out.
type sessionEntry struct {
	sess *session.Session
	tr   *Transport
}

// Transport multiplexes events + audio across all connected WS clients.
// A session can have 0..N WS subscribers (dashboard + call page).
type Transport struct {
	ID string

	mu      sync.Mutex
	clients []*wsClient
	closed  bool
}

type wsClient struct {
	conn   *websocket.Conn
	events chan session.Event
	audio  chan []float32 // PCM48k mono
	ctx    context.Context
}

func (t *Transport) addClient(c *wsClient) {
	t.mu.Lock()
	t.clients = append(t.clients, c)
	t.mu.Unlock()
}

func (t *Transport) removeClient(c *wsClient) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, x := range t.clients {
		if x == c {
			t.clients = append(t.clients[:i], t.clients[i+1:]...)
			close(c.events)
			close(c.audio)
			return
		}
	}
}

func (t *Transport) Emit(e session.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	for _, c := range t.clients {
		select {
		case c.events <- e:
		default:
		}
	}
}

func (t *Transport) BroadcastAudio(pcm48k []float32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	for _, c := range t.clients {
		select {
		case c.audio <- pcm48k:
		default:
		}
	}
}

func (t *Transport) Close() {
	t.mu.Lock()
	t.closed = true
	for _, c := range t.clients {
		_ = c.conn.Close(websocket.StatusNormalClosure, "session ended")
	}
	t.clients = nil
	t.mu.Unlock()
}

// ---------- HTTP handlers ----------

// handleSessionWarmup preloads every model the upcoming call needs so the
// user doesn't pay cold-start latency on their first utterance. Runs LLM
// kv-cache prime, TTS model page-in, and STT health in parallel. Returns a
// per-component status map plus an overall ok bool. Never takes longer than
// ~12s — callers are expected to poll until ok=true.
func handleSessionWarmup(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		type result struct {
			name   string
			ok     bool
			ms     int64
			errMsg string
		}
		results := make(chan result, 3)

		// LLM: fire a 1-token generate to load the model into kv-cache.
		go func() {
			start := time.Now()
			err := d.LLM.Warmup(ctx)
			res := result{name: "llm", ok: err == nil, ms: time.Since(start).Milliseconds()}
			if err != nil {
				res.errMsg = err.Error()
			}
			results <- res
		}()
		// TTS: spawn piper with a tiny utterance and drain the audio (discarded).
		go func() {
			start := time.Now()
			err := d.TTS.Warmup(ctx)
			res := result{name: "tts", ok: err == nil, ms: time.Since(start).Milliseconds()}
			if err != nil {
				res.errMsg = err.Error()
			}
			results <- res
		}()
		// STT: just /health ping.
		go func() {
			start := time.Now()
			err := d.STT.Ping(ctx)
			res := result{name: "stt", ok: err == nil, ms: time.Since(start).Milliseconds()}
			if err != nil {
				res.errMsg = err.Error()
			}
			results <- res
		}()

		status := map[string]any{}
		allOK := true
		for i := 0; i < 3; i++ {
			res := <-results
			entry := map[string]any{"ok": res.ok, "ms": res.ms}
			if res.errMsg != "" {
				entry["err"] = res.errMsg
			}
			status[res.name] = entry
			if !res.ok {
				allOK = false
			}
		}
		writeJSON(w, 200, map[string]any{"ok": allOK, "status": status})
	}
}

func handleSessionStart(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ClientID string `json:"client_id"`
		}
		_ = readJSON(r, &body)
		conv, err := d.Mem.CreateConversation(body.ClientID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		sess := session.New(conv.ID, body.ClientID, conv.ID, &session.Deps{
			Store: d.Store, Mem: d.Mem, LLM: d.LLM, STT: d.STT, TTS: d.TTS, KB: d.KB,
		})
		tr := &Transport{ID: conv.ID}
		entry := &sessionEntry{sess: sess, tr: tr}
		sess.Emit = tr.Emit
		sess.AudioOut = tr.BroadcastAudio
		d.Sessions.Store(conv.ID, entry)
		writeJSON(w, 201, map[string]any{"session_id": conv.ID, "conversation_id": conv.ID})
	}
}

func handleSessionEnd(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "id")
		v, ok := d.Sessions.LoadAndDelete(sid)
		if !ok {
			// Idempotent: the WS `hangup` control message may have already
			// reaped the session. Return 200 so the client doesn't blow up.
			writeJSON(w, 200, map[string]any{"ok": true, "already_ended": true})
			return
		}
		entry := v.(*sessionEntry)
		entry.sess.End()
		entry.tr.Close()
		writeJSON(w, 200, map[string]any{"ok": true})
	}
}

// handleSessionWS is the single duplex endpoint per call.
//
// Inbound (browser -> server):
//   - Binary frame: int16 LE mono 16 kHz PCM (mic audio)
//   - Text frame: JSON {"type":"text_turn","text":"..."} or {"type":"hangup"}
//
// Outbound (server -> browser):
//   - Text frame: JSON event ({"type":"event","event":{...}})
//   - Binary frame: int16 LE mono 48 kHz WAV (TTS audio), prefixed with 16 bytes:
//         0..3  magic "APCM"
//         4..7  big-endian uint32 sample rate (48000)
//         8..11 big-endian uint32 num samples
//         12..15 reserved
//     followed by int16 LE samples. This keeps framing explicit for the browser.
func handleSessionWS(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sid := chi.URLParam(r, "id")
		v, ok := d.Sessions.Load(sid)
		if !ok {
			writeErr(w, 404, "session not found")
			return
		}
		entry := v.(*sessionEntry)
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
			CompressionMode:    websocket.CompressionDisabled,
		})
		if err != nil {
			return
		}
		c.SetReadLimit(1 << 20)

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		client := &wsClient{
			conn:   c,
			events: make(chan session.Event, 64),
			audio:  make(chan []float32, 32),
			ctx:    ctx,
		}
		entry.tr.addClient(client)
		defer entry.tr.removeClient(client)

		// Writer goroutine: events + audio.
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case e, ok := <-client.events:
					if !ok {
						return
					}
					msg, _ := json.Marshal(map[string]any{"type": "event", "event": e})
					wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
					if err := c.Write(wctx, websocket.MessageText, msg); err != nil {
						wcancel()
						return
					}
					wcancel()
				case a, ok := <-client.audio:
					if !ok {
						return
					}
					payload := encodeAudioFrame(a)
					wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
					if err := c.Write(wctx, websocket.MessageBinary, payload); err != nil {
						wcancel()
						return
					}
					wcancel()
				}
			}
		}()

		// Reader: mic frames + control messages.
		for {
			typ, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			switch typ {
			case websocket.MessageBinary:
				handleMicFrame(entry.sess, b)
			case websocket.MessageText:
				var msg struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if err := json.Unmarshal(b, &msg); err != nil {
					continue
				}
				switch msg.Type {
				case "text_turn":
					entry.sess.HandleTextTurn(context.Background(), msg.Text)
				case "hangup":
					entry.sess.End()
					entry.tr.Close()
					d.Sessions.Delete(sid)
					return
				}
			}
		}
	}
}

// handleMicFrame decodes browser mic payload (int16 LE @ 16 kHz mono).
func handleMicFrame(sess *session.Session, b []byte) {
	if len(b) < 2 {
		return
	}
	samples := make([]float32, len(b)/2)
	for i := range samples {
		s := int16(binary.LittleEndian.Uint16(b[i*2:]))
		samples[i] = float32(s) / 32768.0
	}
	sess.WriteMic(samples)
}

// encodeAudioFrame turns float32 PCM48k mono into the APCM wire format.
func encodeAudioFrame(pcm48k []float32) []byte {
	n := len(pcm48k)
	out := make([]byte, 16+n*2)
	copy(out[0:4], []byte("APCM"))
	binary.BigEndian.PutUint32(out[4:8], 48000)
	binary.BigEndian.PutUint32(out[8:12], uint32(n))
	for i, s := range pcm48k {
		if s > 1 {
			s = 1
		} else if s < -1 {
			s = -1
		}
		v := int16(s * 32767)
		binary.LittleEndian.PutUint16(out[16+i*2:], uint16(v))
	}
	return out
}

// Used by debug/export routes elsewhere.
func convertPcmToWavBytes(pcm16k []float32) []byte { return audio.Float32ToWav(pcm16k, 16000) }

var _ = convertPcmToWavBytes
