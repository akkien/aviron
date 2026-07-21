import { Fragment, useEffect, useRef, useState } from "react"

import { playErrorClick, playKeyClick } from "@/lib/keyboardSound"

interface TypingBoxProps {
  promptText: string
  distanceMeters: number
  sendTelemetry: (seq: number, wordsCorrect: number, paceWatt: number) => void
}

// Fixed, explicit line height in px — set directly via inline style below
// (not a Tailwind leading-* class) and reused as-is in the scroll effect's
// math, rather than reading it back from getComputedStyle. Tailwind's
// leading-loose is an unitless "2", and CSS's computed value for a unitless
// line-height stays unitless (per spec) rather than resolving to px — so
// getComputedStyle(...).lineHeight can come back as the literal string "2",
// which parseFloat happily reads as *2 pixels*, not 2x the font size. That
// silently broke the "only scroll on a real line change" math (dividing by
// 2px instead of ~36px made almost every render register as a new line).
// One shared constant sidesteps the whole class of bug.
const LINE_HEIGHT_PX = 36

// TypingBox is a word-by-word validator, ported from the mockup's own
// reference implementation (TypingRace.dc.html's onInputChange/onKeyDown) —
// replaces TypingView's old countCompletedWords, which counted any
// whitespace-separated token as "done" regardless of correctness. The
// server still never inspects typed text (project-overview.md §13); this is
// purely a client-side gate on *when* the client itself decides a word is
// done, so the wire protocol (one telemetry message per correctly-completed
// word) is unchanged.
//
// A wrong keystroke is rejected outright (not inserted, no state change) —
// the player can only ever type the correct next letter. This replaced an
// earlier "accept anything, flag mismatches red" version: letting wrong
// keystrokes accumulate meant every failed Space (silently swallowed) could
// snowball many wrong attempts into one giant unbroken string, making
// Backspace feel "stuck" clawing back through it (real user report).
// Blocking at the source means `input` is always a guaranteed-correct
// prefix of the current word, so there's no mismatch state to render or
// correct in the first place.
export function TypingBox({ promptText, distanceMeters, sendTelemetry }: TypingBoxProps) {
  const words = promptText.split(" ")
  const [wordIndex, setWordIndex] = useState(0)
  const [input, setInput] = useState("")
  // error only drives the current-letter indicator's color (red vs blue) —
  // the wrong keystroke itself is still never inserted into input.
  const [error, setError] = useState(false)

  const inputRef = useRef<HTMLInputElement>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const currentWordRef = useRef<HTMLSpanElement>(null)
  const seqRef = useRef(0)
  const startedAtRef = useRef<number | null>(null)
  // The current word's last-known vertical offset (in px, within the
  // scrollable content) — lets the scroll effect tell "still on this
  // line" apart from "just wrapped to a new one".
  const currentLineTopRef = useRef<number | null>(null)

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  // Keeps the current word's line pinned as the topmost visible line — but
  // only scrolls when the word has actually moved to a new line, i.e. once
  // a whole line is finished, not on every word within the same line.
  //
  // Scrolls to the word's *exact measured* offset, not `lineIndex *
  // LINE_HEIGHT_PX` — an earlier version multiplied an assumed line index
  // by LINE_HEIGHT_PX to get the scroll target, but the browser's real
  // rendered line spacing didn't match that constant closely enough, so
  // the target consistently undershot: the previous line's tail stayed
  // visible at the top, overlapping the new current line instead of a
  // clean cut (reported as "cannot type anymore" — the cursor being
  // tracked was smeared across two overlapping lines). Measuring the real
  // position directly removes any dependency on that constant being
  // exact; it's only used below as a coarse "did this really move to a
  // new line" threshold, which doesn't need to be precise.
  //
  // The scroll target also subtracts the container's own padding-top: a
  // scroll container's top padding only ever creates a visible gap at
  // scrollTop 0 — line 0 sits below it, but scrolling to any line's raw
  // content offset lands that padding-top *before* the scrolled-away
  // content, not above the new first line, so every line after the first
  // would otherwise sit flush against the border with no breathing room.
  useEffect(() => {
    const container = containerRef.current
    const wordEl = currentWordRef.current
    if (!container || !wordEl) return

    const relativeTop =
      wordEl.getBoundingClientRect().top - container.getBoundingClientRect().top + container.scrollTop

    if (currentLineTopRef.current === null) {
      currentLineTopRef.current = relativeTop
      return
    }

    if (Math.abs(relativeTop - currentLineTopRef.current) < LINE_HEIGHT_PX / 2) return

    currentLineTopRef.current = relativeTop
    const paddingTop = parseFloat(getComputedStyle(container).paddingTop) || 0
    container.scrollTo({ top: relativeTop - paddingTop, behavior: "smooth" })
  }, [wordIndex])

  function focusInput() {
    inputRef.current?.focus()
  }

  // Defensive backstop for paste/IME input that bypasses handleKeyDown's
  // per-character gate — only ever accept a change that keeps input a valid
  // prefix of the current word; anything else is silently dropped (the
  // controlled <input> snaps back to the last-good value on re-render).
  function handleChange(e: React.ChangeEvent<HTMLInputElement>) {
    const val = e.target.value
    const word = words[wordIndex] ?? ""
    if (word.startsWith(val)) {
      setInput(val)
    }
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    const word = words[wordIndex] ?? ""

    // Space is also length-1 but is handled separately below (word-advance,
    // not a letter to match) — excluding it here is what was missing.
    if (e.key.length === 1 && e.key !== " ") {
      // Reject anything but the correct next letter — do nothing until the
      // right key is pressed, instead of accepting-and-flagging a mismatch.
      // error still flips on so the current-letter indicator turns red,
      // giving feedback without ever inserting the wrong character.
      if (e.key !== word[input.length]) {
        e.preventDefault()
        setError(true)
        playErrorClick()
        return
      }
      setError(false)
      playKeyClick()
      return
    }

    if (e.key === "Backspace") {
      setError(false)
      playKeyClick()
      return
    }

    if (e.key !== " ") return
    e.preventDefault()

    if (input.length === 0) return
    // input is always a correct prefix by construction — this only fires
    // on a premature Space before the word is fully typed.
    if (input !== word) return

    const newIndex = wordIndex + 1
    setWordIndex(newIndex)
    setInput("")
    setError(false)

    const wordsCompleted = Math.min(newIndex, distanceMeters)
    if (startedAtRef.current === null) startedAtRef.current = Date.now()
    const elapsedMinutes = (Date.now() - startedAtRef.current) / 60000
    const paceWatt = elapsedMinutes > 0 ? Math.round(wordsCompleted / elapsedMinutes) : 0

    seqRef.current += 1
    sendTelemetry(seqRef.current, wordsCompleted, paceWatt)
  }

  const word = words[wordIndex] ?? ""
  const doneWords = words.slice(0, wordIndex)
  const pendingWords = words.slice(wordIndex + 1)
  const finished = wordIndex >= words.length

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2.5">
      <div className="text-xs font-bold uppercase tracking-wider text-muted-foreground">
        Type To Move
      </div>
      <div
        ref={containerRef}
        onClick={focusInput}
        style={{ lineHeight: `${LINE_HEIGHT_PX}px` }}
        className="h-48 cursor-text overflow-hidden rounded-2xl border-[3px] border-input bg-card p-4 font-mono text-lg font-semibold"
      >
        {/* Word gaps must be real space characters, not margin. CSS only
            wraps at actual whitespace (or other explicit break points) —
            margin between two adjacent inline spans creates a visual gap
            but no break opportunity, so with zero literal spaces anywhere
            in the DOM the whole prompt rendered as one unbroken inline run
            and silently overflowed past the right edge instead of wrapping
            (clipped by overflow-hidden). The space is a plain text node
            sibling *after* each colored span, not inside it, so highlight
            backgrounds/underlines don't smear onto the gap. */}
        {/* No horizontal padding on the done-word span: a word must be the
            exact same width in every state (pending → current → done), or
            the line's total width keeps growing as words complete, which
            silently pushes the wrap point earlier than where it first
            rendered — the line visibly "gets wider" and re-wraps mid-type. */}
        {doneWords.map((w, i) => (
          <Fragment key={i}>
            <span className="rounded bg-green-100 text-green-600">{w}</span>{" "}
          </Fragment>
        ))}
        {!finished && (
          <Fragment>
            {/* Plain inline (no display override) — it must size to its own
                content and wrap with the rest of the text like every other
                word, not stretch to fill the line the way inline-flex did. */}
            <span ref={currentWordRef}>
              {word.split("").map((ch, i) => {
                let className = "text-foreground"
                if (i < input.length) {
                  className = "text-green-600 bg-green-100"
                } else if (i === input.length) {
                  // The very next letter to type — blue+underline normally,
                  // red background+underline right after a rejected wrong
                  // keystroke. Background (not just text color) matters
                  // once the "letter" is actually the trailing space
                  // below: a red space glyph is invisible, only a painted
                  // background shows.
                  className = error
                    ? "text-destructive bg-red-100 underline decoration-2 underline-offset-2"
                    : "text-primary underline decoration-2 underline-offset-2"
                }
                return (
                  <span key={i} className={className}>
                    {ch}
                  </span>
                )
              })}
            </span>
            {/* The gap after the word doubles as the cursor once every
                letter is correct and Space is the only valid next key — a
                wrong keystroke here (e.g. a letter instead of advancing)
                still sets `error`, but there's no letter glyph to redden,
                so paint the space itself instead of leaving the mistake
                invisible. */}
            <span className={input.length === word.length && error ? "bg-red-100" : undefined}>
              {" "}
            </span>
          </Fragment>
        )}
        {pendingWords.map((w, i) => (
          <Fragment key={i}>
            <span className="text-muted-foreground/70">{w}</span>{" "}
          </Fragment>
        ))}
        {/* A scroll container can never scroll further than its actual
            content height, so near the end of the prompt there isn't
            enough text left below the current line to push it all the way
            to the top — the browser clamps the scroll and several lines
            stay visible instead of just the current one. This spacer pads
            the scrollable content so there's always at least one
            container's worth of room below any line, including the very
            last one, so it can still be scrolled flush to the top. */}
        <div aria-hidden className="h-48" />
      </div>
      <input
        ref={inputRef}
        value={input}
        onChange={handleChange}
        onKeyDown={handleKeyDown}
        className="pointer-events-none absolute h-px w-px opacity-0"
      />
      {finished && (
        <div className="text-center font-heading text-lg font-bold text-primary">
          🏁 You finished the race!
        </div>
      )}
    </div>
  )
}
