// Race-lane colors, cycled per participant by index so each car in the
// typing view's lanes is visually distinct. #3A59D1 is also the app's
// primary blue (see src/index.css) — kept in the cycle anyway since it's
// still a valid lane color, just first in line.
export const RACE_LANE_COLORS: readonly string[] = [
  "#3A59D1",
  "#3D90D7",
  "#328E6E",
  "#67AE6E",
  "#1F6F5F",
  "#8F87F1",
  "#C68EFD",
  "#EC7FA9",
  "#FFB8E0",
  "#E25E3E",
  "#FFBB5C",
  "#80A1BA",
  "#7E99A3",
  "#54473F",
  "#FF3737",
  "#F9ED69",
  "#FFEDC6",
  "#FFB19B",
]

export function laneColor(index: number): string {
  return RACE_LANE_COLORS[index % RACE_LANE_COLORS.length]
}
