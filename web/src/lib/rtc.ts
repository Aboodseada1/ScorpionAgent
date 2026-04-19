// Live-call transport for the browser. It owns:
//   - getUserMedia mic with echoCancellation ON (browser AEC3 = echo layer 1)
//   - An AudioWorklet that downsamples mic 48k -> 16k PCM16 and posts frames
//   - A WebSocket to the session (binary mic -> server, binary TTS <- server)
//   - A local AudioContext sink that plays incoming TTS immediately
//
// Echo story:
//   - Layer 1: `echoCancellation: true` pins the browser's built-in AEC against
//     system output. Chrome's AEC3 uses the default audio output as reference,
//     so even with plain speakers echo is suppressed before we capture a frame.
//   - Layer 2: server-side TTS gate (in Go `vad` package) raises the threshold
//     and subtracts the TTS reference while we're speaking.
//   - Layer 3: server-side n-gram self-filter drops any transcript that repeats
//     our own recent TTS.

export type EventCallback = (e: { kind: string; payload?: unknown; t?: number }) => void;

export class CallTransport {
  ws?: WebSocket;
  audioCtx?: AudioContext;
  worklet?: AudioWorkletNode;
  micStream?: MediaStream;
  onEvent: EventCallback = () => {};
  onStateChange: (state: "idle" | "connecting" | "live" | "ended") => void = () => {};
  /**
   * Live mic-level callback, 0..1 RMS, fired ~30x/sec. Used by the UI ring
   * pulse so the user sees real-time feedback while speaking.
   */
  onMicLevel: (level: number) => void = () => {};
  /**
   * Live AI audio level (RMS 0..1 of the most recent TTS chunk) so the UI
   * can animate the AI avatar while it's speaking.
   */
  onAILevel: (level: number) => void = () => {};
  private micAnalyser?: AnalyserNode;
  private levelRAF = 0;
  private ttsQueue: AudioBuffer[] = [];
  private ttsScheduledTime = 0;
  /** Mutes all TTS routed through this node (barge-in / interrupt). */
  private ttsGain?: GainNode;
  /** When false, Piper never reaches speakers (stops laptop-mic echo into Web Speech). */
  private assistantSpeakersOn = false;
  /** After `muteIncomingTTS`, restore only when this is cleared in `unmuteIncomingTTS`. */
  private bargeDucked = false;
  private disposed = false;
  private announced: "idle" | "connecting" | "live" | "ended" = "idle";

  private setState(s: "idle" | "connecting" | "live" | "ended"): void {
    if (this.announced === s) return;
    this.announced = s;
    this.onStateChange(s);
  }

  /**
   * @param playAssistantAudio When false (default), TTS is decoded for levels/UI but not sent to
   *   `audioCtx.destination` — required for laptop speakers + browser dictation without echo.
   */
  async start(
    sessionID: string,
    options?: { playAssistantAudio?: boolean },
  ): Promise<void> {
    this.setState("connecting");
    this.assistantSpeakersOn = options?.playAssistantAudio ?? false;
    this.bargeDucked = false;
    // 1) get mic with AEC+NS+AGC on
    this.micStream = await navigator.mediaDevices.getUserMedia({
      audio: {
        echoCancellation: true,
        noiseSuppression: true,
        autoGainControl: true,
        channelCount: 1,
      },
      video: false,
    });

    // 2) build AudioContext and worklet to downsample mic to 16 kHz PCM16
    this.audioCtx = new AudioContext({ sampleRate: 48000 });
    this.ttsGain = this.audioCtx.createGain();
    this.ttsGain.gain.value = this.assistantSpeakersOn ? 1 : 0;
    this.ttsGain.connect(this.audioCtx.destination);

    const workletURL = URL.createObjectURL(new Blob([MIC_WORKLET_SRC], { type: "text/javascript" }));
    await this.audioCtx.audioWorklet.addModule(workletURL);
    const src = this.audioCtx.createMediaStreamSource(this.micStream);
    this.worklet = new AudioWorkletNode(this.audioCtx, "mic-downsampler");
    src.connect(this.worklet);

    // Side-chain the mic into an analyser so we can emit an RMS level to the
    // UI in real time. This doesn't affect what's sent to the server — it's
    // purely for the ring pulse around the user's avatar.
    this.micAnalyser = this.audioCtx.createAnalyser();
    this.micAnalyser.fftSize = 512;
    this.micAnalyser.smoothingTimeConstant = 0.2;
    src.connect(this.micAnalyser);
    const levelBuf = new Float32Array(this.micAnalyser.fftSize);
    const tick = () => {
      if (this.disposed || !this.micAnalyser) return;
      this.micAnalyser.getFloatTimeDomainData(levelBuf);
      let sumSq = 0;
      for (let i = 0; i < levelBuf.length; i++) sumSq += levelBuf[i] * levelBuf[i];
      const rms = Math.sqrt(sumSq / levelBuf.length);
      this.onMicLevel(Math.min(1, rms * 6));
      this.levelRAF = requestAnimationFrame(tick);
    };
    this.levelRAF = requestAnimationFrame(tick);

    // 3) connect WS
    const proto = location.protocol === "https:" ? "wss" : "ws";
    this.ws = new WebSocket(`${proto}://${location.host}/api/session/${sessionID}/ws`);
    this.ws.binaryType = "arraybuffer";

    // 4) wire up
    this.ws.onopen = () => {
      this.setState("live");
      this.worklet!.port.onmessage = (ev) => {
        if (!(this.ws && this.ws.readyState === WebSocket.OPEN)) return;
        const frame = ev.data as ArrayBuffer;
        try { this.ws.send(frame); } catch { /* closed mid-frame */ }
      };
    };
    this.ws.onmessage = (ev) => {
      if (typeof ev.data === "string") {
        try {
          const msg = JSON.parse(ev.data);
          if (msg.type === "event") this.onEvent(msg.event);
        } catch {
          /* ignore */
        }
      } else {
        this.enqueueAudio(ev.data as ArrayBuffer);
      }
    };
    this.ws.onclose = () => this.setState("ended");
    this.ws.onerror = () => this.setState("ended");
  }

  sendText(text: string): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
    try { this.ws.send(JSON.stringify({ type: "text_turn", text })); } catch { /* noop */ }
  }

  setMuted(muted: boolean): void {
    this.micStream?.getAudioTracks().forEach((t) => {
      t.enabled = !muted;
    });
  }

  /** Route Piper output to the tab speakers (off by default to avoid mic echo with Web Speech). */
  setAssistantSpeakerOutput(on: boolean): void {
    this.assistantSpeakersOn = on;
    if (this.disposed || this.bargeDucked || !this.ttsGain || !this.audioCtx) return;
    const g = this.ttsGain;
    const t = this.audioCtx.currentTime;
    const v = on ? 1 : 0;
    try {
      g.gain.cancelScheduledValues(t);
      g.gain.setValueAtTime(v, t);
    } catch {
      g.gain.value = v;
    }
  }

  /** Instantly silence queued/playing assistant audio (server barge-in). Next `tts_start` unmutes. */
  muteIncomingTTS(): void {
    const g = this.ttsGain;
    const ctx = this.audioCtx;
    if (!g || !ctx) return;
    this.bargeDucked = true;
    const t = ctx.currentTime;
    try {
      g.gain.cancelScheduledValues(t);
      g.gain.setValueAtTime(0, t);
    } catch {
      g.gain.value = 0;
    }
    this.ttsScheduledTime = t;
  }

  /** Restore TTS playback after `muteIncomingTTS` (call on `tts_start`). */
  unmuteIncomingTTS(): void {
    const g = this.ttsGain;
    const ctx = this.audioCtx;
    if (!g || !ctx) return;
    this.bargeDucked = false;
    const t = ctx.currentTime;
    const v = this.assistantSpeakersOn ? 1 : 0;
    try {
      g.gain.cancelScheduledValues(t);
      g.gain.setValueAtTime(v, t);
    } catch {
      g.gain.value = v;
    }
  }

  hangup(): void {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      try { this.ws.send(JSON.stringify({ type: "hangup" })); } catch { /* noop */ }
    }
    this.cleanup();
  }

  cleanup(): void {
    if (this.disposed) return;
    this.disposed = true;
    if (this.levelRAF) cancelAnimationFrame(this.levelRAF);
    try {
      if (this.ws && this.ws.readyState !== WebSocket.CLOSED && this.ws.readyState !== WebSocket.CLOSING) {
        this.ws.close();
      }
    } catch { /* noop */ }
    try { this.worklet?.port.close(); } catch { /* noop */ }
    try { this.worklet?.disconnect(); } catch { /* noop */ }
    try { this.micAnalyser?.disconnect(); } catch { /* noop */ }
    this.micAnalyser = undefined;
    if (this.audioCtx && this.audioCtx.state !== "closed") {
      try { void this.audioCtx.close(); } catch { /* noop */ }
    }
    this.micStream?.getTracks().forEach((t) => t.stop());
    this.micStream = undefined;
    this.worklet = undefined;
    this.audioCtx = undefined;
    this.ws = undefined;
    this.setState("ended");
  }

  private enqueueAudio(ab: ArrayBuffer): void {
    if (!this.audioCtx) return;
    const dv = new DataView(ab);
    if (dv.byteLength < 16) return;
    const magic = String.fromCharCode(dv.getUint8(0), dv.getUint8(1), dv.getUint8(2), dv.getUint8(3));
    if (magic !== "APCM") return;
    const sr = dv.getUint32(4, false);
    const n = dv.getUint32(8, false);
    if (16 + n * 2 > dv.byteLength) return;
    const buf = this.audioCtx.createBuffer(1, n, sr);
    const ch = buf.getChannelData(0);
    let sumSq = 0;
    for (let i = 0; i < n; i++) {
      const s = dv.getInt16(16 + i * 2, true);
      const f = s / 32768;
      ch[i] = f;
      sumSq += f * f;
    }
    const rms = n > 0 ? Math.sqrt(sumSq / n) : 0;
    this.onAILevel(Math.min(1, rms * 4));
    const now = this.audioCtx.currentTime;
    const when = Math.max(now + 0.02, this.ttsScheduledTime);
    const srcNode = this.audioCtx.createBufferSource();
    srcNode.buffer = buf;
    srcNode.connect(this.ttsGain!);
    srcNode.start(when);
    this.ttsScheduledTime = when + buf.duration;
    this.ttsQueue.push(buf);
    if (this.ttsQueue.length > 64) this.ttsQueue.shift();
  }
}

const MIC_WORKLET_SRC = `
class MicDownsampler extends AudioWorkletProcessor {
  constructor() {
    super();
    this.buffer = [];
    this.acc = 0;
    this.ratio = sampleRate / 16000;
  }
  process(inputs) {
    const input = inputs[0];
    if (!input || !input[0]) return true;
    const ch = input[0];
    for (let i = 0; i < ch.length; i++) {
      this.acc += 1;
      if (this.acc >= this.ratio) {
        this.acc -= this.ratio;
        let s = ch[i];
        if (s > 1) s = 1; else if (s < -1) s = -1;
        this.buffer.push(s * 32767);
      }
    }
    // Send every ~20 ms @ 16 kHz (320 samples) — faster VAD updates / lower latency
    while (this.buffer.length >= 320) {
      const out = new Int16Array(320);
      for (let i = 0; i < 320; i++) out[i] = this.buffer[i] | 0;
      this.buffer.splice(0, 320);
      this.port.postMessage(out.buffer, [out.buffer]);
    }
    return true;
  }
}
registerProcessor("mic-downsampler", MicDownsampler);
`;
