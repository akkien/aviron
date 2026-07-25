// Flat cartoon-style top-down water surface: a solid water-color fill with
// soft, semi-transparent "ribbon" bands and small white bubble dots
// scattered throughout — no noise/grain texture, matching a flat vector
// illustration look rather than a photorealistic one. Ribbons are filled,
// tapered shapes (pointy at both ends, via a sine width profile along the
// centerline curve) rather than round-capped strokes — a round cap reads as
// a blunt tip, and since every full-span ribbon would otherwise share the
// same edge-crossing point, a whole row of round caps could cross into view
// together and look like a mechanical "wall" sweeping through. Pointy tips
// plus a staggered per-ribbon bleed distance (so ribbons don't all cross the
// edge at the same x) avoid that. The whole pattern flows continuously to
// one side: it's laid out once as a "tile" of width VIEWBOX_WIDTH, drawn
// twice side by side, and the pair is translated left by exactly one tile
// width on a loop — since both copies are identical, the loop resets with
// nothing visibly jumping. No raster image involved.
const WATER_COLOR = "#00CAFF"
const VIEWBOX_WIDTH = 1200
const VIEWBOX_HEIGHT = 700
const RIBBON_COUNT = 10
const BUBBLE_COUNT = 26
const FLOW_DURATION_S = 90
const RIBBON_SAMPLES = 20

type Ribbon = {
  x0: number
  x1: number
  y0: number
  y1: number
  c1x: number
  c1y: number
  c2x: number
  c2y: number
  maxWidth: number
  opacity: number
}

type Bubble = {
  cx: number
  cy: number
  rx: number
  ry: number
  opacity: number
  bobAmplitude: number
  bobDuration: number
}

// Deterministic PRNG (mulberry32) so the "randomness" is reproducible
// instead of reshuffling on every render/reload.
function mulberry32(seed: number) {
  let a = seed
  return () => {
    a = (a + 0x6d2b79f5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

function buildRibbons(count: number): Ribbon[] {
  const rand = mulberry32(3)
  const ribbons: Ribbon[] = []
  for (let i = 0; i < count; i++) {
    // Roughly one band per slot of the height, jittered so the rows don't
    // read as an evenly-spaced grid. A real diagonal slope plus an S-bend on
    // the control points (not just a gentle bow) is what keeps these
    // reading as sweeping curves instead of flat horizontal stripes. Most
    // bands bleed off both edges (full sweeping strokes, each by its own
    // randomized amount so they don't all cross the edge together); the
    // rest are short, isolated blobs mid-canvas.
    const slot = VIEWBOX_HEIGHT / count
    const baseY = (i + 0.5) * slot + (rand() - 0.5) * slot * 0.6
    const isFullSpan = rand() < 0.85
    const x0 = isFullSpan ? -(60 + rand() * 260) : rand() * (VIEWBOX_WIDTH - 500)
    const x1 = isFullSpan ? VIEWBOX_WIDTH + 60 + rand() * 260 : x0 + 220 + rand() * 340
    // Diagonal/bow are scaled down for short spans — applying a full-width
    // ribbon's slope/bend to a short isolated blob folds the curve back on
    // itself instead of reading as a gentle rounded shape.
    const spanFactor = Math.max(0.3, Math.min(1, (x1 - x0) / 500))
    const diagonal = (rand() - 0.5) * 280 * spanFactor
    const y0 = baseY - diagonal / 2
    const y1 = baseY + diagonal / 2
    const bow = (35 + rand() * 85) * spanFactor
    const bowDir = rand() > 0.5 ? 1 : -1
    ribbons.push({
      x0,
      x1,
      y0,
      y1,
      c1x: x0 + (x1 - x0) * 0.33,
      c2x: x0 + (x1 - x0) * 0.66,
      c1y: y0 + diagonal * 0.15 + bowDir * bow,
      c2y: y1 - diagonal * 0.15 - bowDir * bow,
      maxWidth: (isFullSpan ? 28 : 46) + rand() * 40,
      opacity: 0.2 + rand() * 0.22,
    })
  }
  return ribbons
}

function cubicPoint(p0: number, p1: number, p2: number, p3: number, t: number): number {
  const mt = 1 - t
  return mt * mt * mt * p0 + 3 * mt * mt * t * p1 + 3 * mt * t * t * p2 + t * t * t * p3
}

function cubicTangent(p0: number, p1: number, p2: number, p3: number, t: number): number {
  const mt = 1 - t
  return 3 * mt * mt * (p1 - p0) + 6 * mt * t * (p2 - p1) + 3 * t * t * (p3 - p2)
}

// Filled outline that follows the ribbon's centerline curve, tapering from
// zero width at t=0/t=1 (a genuine point, not a capped stroke) up to
// maxWidth at the midpoint via a sine profile.
function ribbonLeafPath(r: Ribbon): string {
  const top: string[] = []
  const bottom: string[] = []
  for (let i = 0; i <= RIBBON_SAMPLES; i++) {
    const t = i / RIBBON_SAMPLES
    const x = cubicPoint(r.x0, r.c1x, r.c2x, r.x1, t)
    const y = cubicPoint(r.y0, r.c1y, r.c2y, r.y1, t)
    const dx = cubicTangent(r.x0, r.c1x, r.c2x, r.x1, t)
    const dy = cubicTangent(r.y0, r.c1y, r.c2y, r.y1, t)
    const len = Math.hypot(dx, dy) || 1
    const nx = -dy / len
    const ny = dx / len
    const taper = Math.sin(Math.PI * t) * (r.maxWidth / 2)
    top.push(`${(x + nx * taper).toFixed(1)} ${(y + ny * taper).toFixed(1)}`)
    bottom.push(`${(x - nx * taper).toFixed(1)} ${(y - ny * taper).toFixed(1)}`)
  }
  bottom.reverse()
  return `M ${top[0]} L ${top.slice(1).join(" L ")} L ${bottom.join(" L ")} Z`
}

function buildBubbles(count: number): Bubble[] {
  const rand = mulberry32(99)
  const bubbles: Bubble[] = []
  for (let i = 0; i < count; i++) {
    const rx = rand() < 0.15 ? 9 + rand() * 6 : 3 + rand() * 5
    bubbles.push({
      cx: rand() * VIEWBOX_WIDTH,
      cy: rand() * VIEWBOX_HEIGHT,
      rx,
      ry: rx * (0.8 + rand() * 0.2),
      opacity: 0.85 + rand() * 0.15,
      bobAmplitude: 4 + rand() * 8,
      bobDuration: 6 + rand() * 8,
    })
  }
  return bubbles
}

const RIBBONS = buildRibbons(RIBBON_COUNT)
const BUBBLES = buildBubbles(BUBBLE_COUNT)

function WaterTile() {
  return (
    <>
      <g>
        {RIBBONS.map((r, i) => (
          <path key={`ribbon-${i}`} d={ribbonLeafPath(r)} fill="#ffffff" fillOpacity={r.opacity} />
        ))}
      </g>
      <g>
        {BUBBLES.map((b, i) => (
          <ellipse
            key={`bubble-${i}`}
            cx={b.cx}
            cy={b.cy}
            rx={b.rx}
            ry={b.ry}
            fill="#ffffff"
            fillOpacity={b.opacity}
          >
            <animateTransform
              attributeName="transform"
              type="translate"
              values={`0 0; 0 ${-b.bobAmplitude}; 0 0`}
              dur={`${b.bobDuration}s`}
              repeatCount="indefinite"
            />
          </ellipse>
        ))}
      </g>
    </>
  )
}

export function WaterBackground() {
  return (
    <div
      className="pointer-events-none fixed inset-0 -z-10 overflow-hidden"
      style={{ backgroundColor: WATER_COLOR }}
    >
      <svg
        className="h-full w-full"
        viewBox={`0 0 ${VIEWBOX_WIDTH} ${VIEWBOX_HEIGHT}`}
        preserveAspectRatio="none"
        aria-hidden="true"
      >
        <g>
          <g transform="translate(0, 0)">
            <WaterTile />
          </g>
          <g transform={`translate(${VIEWBOX_WIDTH}, 0)`}>
            <WaterTile />
          </g>
          <animateTransform
            attributeName="transform"
            type="translate"
            from="0 0"
            to={`${-VIEWBOX_WIDTH} 0`}
            dur={`${FLOW_DURATION_S}s`}
            repeatCount="indefinite"
          />
        </g>
      </svg>
    </div>
  )
}
