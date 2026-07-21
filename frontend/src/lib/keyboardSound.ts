import mechanicKeyboardUrl from "./mechanic-keyboard.mp3"

// Correct-keystroke click uses the real recorded sample (user-supplied
// file, not scraped — copying typing.com's own sample was explicitly
// declined earlier in this project's history). The source clip is ~1s
// long but only the very start is the actual click; the rest is
// silence/tail, so every play is trimmed to just the leading edge rather
// than the whole clip — otherwise fast typing would either overlap
// awkwardly or feel sluggish waiting out a second of mostly-silence per
// keystroke. AudioContext is created lazily, on the first call, which
// always happens from a keydown handler — a real user gesture, satisfying
// browsers' autoplay-policy requirement to create/resume the context.
const CLICK_TRIM_SECONDS = 0.15
const CLICK_FADE_SECONDS = 0.02

let audioCtx: AudioContext | null = null
// Decoding happens once and is cached as a promise (not just the eventual
// AudioBuffer) so keystrokes that land before the first decode finishes
// don't each kick off their own redundant fetch/decode.
let clickBufferPromise: Promise<AudioBuffer> | null = null
let noiseBuffer: AudioBuffer | null = null

function getAudioContext(): AudioContext {
  if (!audioCtx) {
    const Ctor =
      window.AudioContext ??
      (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext
    audioCtx = new Ctor()
  }
  if (audioCtx.state === "suspended") {
    void audioCtx.resume()
  }
  return audioCtx
}

function getClickBuffer(ctx: AudioContext): Promise<AudioBuffer> {
  if (!clickBufferPromise) {
    clickBufferPromise = fetch(mechanicKeyboardUrl)
      .then((res) => res.arrayBuffer())
      .then((data) => ctx.decodeAudioData(data))
  }
  return clickBufferPromise
}

// Correct-keystroke click — the real recorded sample, trimmed to its
// leading edge (see CLICK_TRIM_SECONDS above).
export function playKeyClick(): void {
  if (typeof window === "undefined") return

  const ctx = getAudioContext()
  getClickBuffer(ctx)
    .then((buffer) => {
      const now = ctx.currentTime
      const source = ctx.createBufferSource()
      source.buffer = buffer

      const gain = ctx.createGain()
      gain.gain.setValueAtTime(1, now)
      // Ramp to silence just before the hard stop below, so cutting the
      // clip off mid-waveform doesn't itself produce an audible click.
      gain.gain.setValueAtTime(1, now + Math.max(CLICK_TRIM_SECONDS - CLICK_FADE_SECONDS, 0))
      gain.gain.exponentialRampToValueAtTime(0.0001, now + CLICK_TRIM_SECONDS)

      source.connect(gain)
      gain.connect(ctx.destination)
      source.start(now)
      source.stop(now + CLICK_TRIM_SECONDS)
    })
    .catch(() => {
      // Sound is a nice-to-have, not core to typing working — a failed
      // fetch/decode should never surface as a broken keystroke.
    })
}

function getNoiseBuffer(ctx: AudioContext): AudioBuffer {
  if (!noiseBuffer) {
    const length = Math.floor(ctx.sampleRate * 0.03)
    noiseBuffer = ctx.createBuffer(1, length, ctx.sampleRate)
    const data = noiseBuffer.getChannelData(0)
    for (let i = 0; i < length; i++) {
      data[i] = Math.random() * 2 - 1
    }
  }
  return noiseBuffer
}

// Wrong-keystroke feedback stays synthesized — deliberately a distinct
// sound from the real mechanical click above, not just the same click
// played again. Built from a filtered noise burst (the switch leaf's
// sharp high-frequency snap) plus a pitch-dropping tone (the switch/
// keycap body resonance underneath it); randomizing both slightly per
// keystroke avoids the "machine-gun" sameness a fixed tone gets under
// fast, repeated wrong keystrokes.
export function playErrorClick(): void {
  if (typeof window === "undefined") return

  const ctx = getAudioContext()
  const now = ctx.currentTime
  const jitter = () => 0.9 + Math.random() * 0.2

  const noise = ctx.createBufferSource()
  noise.buffer = getNoiseBuffer(ctx)
  const noiseFilter = ctx.createBiquadFilter()
  noiseFilter.type = "bandpass"
  noiseFilter.frequency.value = 3200 * jitter()
  noiseFilter.Q.value = 1.1
  const noiseGain = ctx.createGain()
  noiseGain.gain.setValueAtTime(0.16, now)
  noiseGain.gain.exponentialRampToValueAtTime(0.0001, now + 0.018)
  noise.connect(noiseFilter)
  noiseFilter.connect(noiseGain)
  noiseGain.connect(ctx.destination)
  noise.start(now)
  noise.stop(now + 0.02)

  const body = ctx.createOscillator()
  body.type = "triangle"
  body.frequency.setValueAtTime(190 * jitter(), now)
  body.frequency.exponentialRampToValueAtTime(85, now + 0.04)
  const bodyGain = ctx.createGain()
  bodyGain.gain.setValueAtTime(0.09, now)
  bodyGain.gain.exponentialRampToValueAtTime(0.0001, now + 0.05)
  body.connect(bodyGain)
  bodyGain.connect(ctx.destination)
  body.start(now)
  body.stop(now + 0.05)
}
