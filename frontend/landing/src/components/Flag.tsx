const BOX = { display: 'block', flex: 'none', borderRadius: 2 } as const

export function Flag({ width, height }: { width: number; height: number }) {
  return (
    <svg aria-hidden="true" width={width} height={height} viewBox="0 0 3 2" preserveAspectRatio="xMidYMid slice" style={BOX}>
      <rect width="3" height="2" fill="#008751" />
      <rect x="1" width="1" height="2" fill="#fff" />
    </svg>
  )
}
