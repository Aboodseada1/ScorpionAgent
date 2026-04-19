# Scorpion Agent

A self-hosted voice SDR. You add a client, start a call, and speak to the AI in real time —
the AI plays the sales rep, you play the prospect. It transcribes every turn, logs actions
(schedule, reschedule, qualify, flag objection), builds a per-client memory graph, and never
sends a byte to a cloud provider.

Fully rewritten in Go (previously Python). Single static binary, WebSocket-based realtime audio,
three-layer echo cancellation for open speakers.

```
Browser ─── WS (PCM16 16kHz mic + PCM48k TTS) ───▶ Go server ─┬─▶ whisper.cpp (STT, HTTP)
                                                              ├─▶ llama.cpp  (LLM, OpenAI-compat)
                                                              └─▶ piper     (TTS subprocess)
                                                                   SQLite (unified app.sqlite3)
```

---

## What's inside

- `cmd/agent`            — HTTP+WS server binary.
- `cmd/migrate`          — one-shot importer from the legacy Python SQLite DBs.
- `internal/config`      — env + runtime overrides, hot-patchable from the Settings UI.
- `internal/memory`      — SQLite schema, clients/conversations/turns/facts/actions/docs/embeddings.
- `internal/llm`         — streaming SSE client for any OpenAI-compatible `/v1` endpoint, tool-calling loop.
- `internal/stt`         — whisper.cpp HTTP client (drop-in to `whisper.cpp/server`).
- `internal/tts`         — piper subprocess pool, PCM pump.
- `internal/vad`         — energy VAD + TTS gate with reference subtraction (echo layer 2).
- `internal/session`     — per-call FSM (idle/listening/thinking/speaking/ended), barge-in, self-filter (echo layer 3), post-call extraction.
- `internal/actions`     — LLM tool schemas and local executor.
- `internal/kb`          — per-client + global RAG via `/v1/embeddings`.
- `internal/api`         — chi routes + WebSocket transport.
- `web/`                 — React + Vite + Tailwind SPA (light mode, call-app aesthetic).
- `prompts/sdr.txt`      — the SDR persona prompt.
- `deploy/`              — systemd unit and nginx config.

---

## The echo problem (and the three-layer fix)

Open speakers cause self-transcription: the AI speaks, the mic hears itself, Whisper transcribes
it, and the loop feeds itself. Three stacked layers kill this:

1. **Browser AEC (primary).** `navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true }})`. Chrome/Safari's system-level AEC runs against default audio output, so the reference signal is there even without a full WebRTC peer.
2. **Server TTS gate (secondary).** While Piper is speaking + `TTS_ACOUSTIC_TAIL_MS`, the VAD threshold is multiplied and energy that correlates with the Piper reference is subtracted from the mic before it hits Whisper. See `internal/vad/vad.go`.
3. **Semantic self-filter (tertiary).** Any finalized transcript whose 6-gram shingles overlap with the last TTS is dropped as self-speech. See `internal/session/selffilter.go`.

Barge-in still works: if the user speaks >150ms past the guard, the in-flight LLM stream is cancelled.

Tune via the Settings page or `.env`:
```
VAD_THRESHOLD=0.55
TTS_ACOUSTIC_TAIL_MS=850
BARGE_IN_GUARD_MS=550
SELF_FILTER_NGRAM=6
```

---

## Quickstart

1. **Install prereqs**
   - Go 1.23+
   - Node 20+ (for the web build)
   - `piper` binary and a voice model (e.g., `en_US-lessac-medium.onnx` + `.json`)
   - [whisper.cpp](https://github.com/ggerganov/whisper.cpp) built with the HTTP server (`./server -m models/ggml-base.en.bin --host 0.0.0.0 --port 9000`)
   - [llama.cpp](https://github.com/ggerganov/llama.cpp) running as `llama-server` (e.g., `./llama-server -m qwen2.5.gguf --host 0.0.0.0 --port 8080`)

2. **Copy the env template**
   ```bash
   cp .env.example .env
   $EDITOR .env   # set PIPER_BIN, PIPER_MODEL, LLM_BASE_URL, WHISPER_BASE_URL
   ```

3. **Build everything**
   ```bash
   make            # builds the SPA + the Go binary
   ```

4. **Run**
   ```bash
   ./bin/agent
   # server on :8001 — open http://localhost:8001/
   ```

5. **Migrate legacy Python DBs (optional)**
   ```bash
   ./bin/migrate -dir ./data
   ```

---

## Day-to-day

- Add a client in **Clients**, fill in the business/industry/stage/notes.
- Click **Start call**. Grant mic permission. Say hi as if you're the business owner.
- The AI plays Jamie at Centergrowth. As the conversation progresses:
  - captions appear on both sides in real time,
  - action chips pop up when the AI calls a tool (`schedule_followup`, `reschedule`, `log_qualification`, `draft_email`, `add_note`, `flag_objection`),
  - latency is shown at the bottom.
- Hang up. A post-call LLM extraction pass runs and populates the **Memory graph** for that client.

The **Memory** page shows the full graph across all clients (or filtered). Nodes are clients,
conversations, days, facts, and actions; edges are who-said-what-when.

---

## HTTP API

| Route | Method | Purpose |
|---|---|---|
| `/api/status` | GET | Liveness for LLM / STT / Piper. |
| `/api/settings` | GET / PATCH | Read base config + write runtime overrides. |
| `/api/clients` | GET / POST | List / create clients. |
| `/api/clients/{id}` | GET / PUT / DELETE | CRUD one client. |
| `/api/clients/{id}/docs` | GET / POST | Per-client KB documents. |
| `/api/clients/{id}/conversations` | GET | Calls for this client. |
| `/api/clients/{id}/facts` | GET | Learned facts. |
| `/api/clients/{id}/actions` | GET | Actions the AI took. |
| `/api/conversations/{id}/turns` | GET | Full transcript. |
| `/api/conversations/{id}/graph` | GET | Per-call subgraph. |
| `/api/memory/graph` | GET | Global memory graph. |
| `/api/session/start` | POST | Create a new call session. |
| `/api/session/{id}/ws` | GET (WS) | Duplex mic / TTS / events. |
| `/api/session/{id}/text` | POST | Send a user turn as text (testing). |
| `/api/session/{id}/end` | POST | Hang up and trigger extraction. |

WebSocket wire format:

- **Inbound binary frames** are `int16` LE mono PCM at 16 kHz (mic). The browser's audio worklet does the downsample.
- **Inbound text frames** are JSON control messages: `{"type":"text_turn","text":"..."}` or `{"type":"hangup"}`.
- **Outbound text frames** are events: `{"type":"event","event":{kind, payload, t}}` for `state`, `transcript`, `llm_delta`, `tts_start`, `tts_end`, `action`, `self_filtered`, `usage`, `error`, `extraction_done`, `barge_in`.
- **Outbound binary frames** are TTS audio: `APCM` + big-endian `uint32` sample_rate (48000) + big-endian `uint32` n_samples + 4 reserved bytes + `int16` LE PCM samples.

---

## Configuration

All env vars are optional — sane defaults match a typical local setup. Anything set in the
Settings page writes to `data/runtime_overrides.json` and takes effect without restart.

See `.env.example` for the full list.

---

## Deploy

```bash
sudo cp deploy/agent-voice.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now agent-voice
sudo cp deploy/nginx-agent.scorpion.codes.conf /etc/nginx/sites-available/
sudo ln -sf /etc/nginx/sites-available/nginx-agent.scorpion.codes.conf /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

The nginx upstream flips to `:8001` (the Go binary) — the old Python `:8000` can be retired.

---

## Why we replaced Python

- **Jitter.** Per-frame numpy + asyncio + GIL-bound whisper/piper threads caused spikes we
  couldn't design around.
- **Transport.** WebSocket audio in Go is one file and ~200 lines; the equivalent FastAPI
  path was fighting uvicorn buffering constantly.
- **Deploy.** One static binary instead of a .venv with 31 requirements.
- **Memory.** Three separate SQLite databases (calls / intel / kb) are collapsed into one
  schema with proper foreign keys, which the memory-graph UI needs.

The old Python implementation is gone from the repo; the memory from those calls has been
migrated via `./bin/migrate`.
