import type { CountryId } from '../countries'

const BOX = { display: 'block', flex: 'none', borderRadius: 2 } as const

export function Flag({ id, width, height }: { id: CountryId; width: number; height: number }) {
  const view = id === 'NG' ? '0 0 3 2' : '0 0 30 20'
  return (
    <svg aria-hidden="true" width={width} height={height} viewBox={view} preserveAspectRatio="xMidYMid slice" style={BOX}>
      {id === 'NG' && (
        <>
          <rect width="3" height="2" fill="#008751" />
          <rect x="1" width="1" height="2" fill="#fff" />
        </>
      )}
      {id === 'KE' && (
        <>
          <rect width="30" height="20" fill="#006600" />
          <rect width="30" height="14" fill="#fff" />
          <rect width="30" height="6" fill="#000" />
          <rect y="7" width="30" height="6" fill="#bb0000" />
        </>
      )}
      {id === 'ZA' && (
        <>
          <rect width="30" height="10" fill="#de3831" />
          <rect y="10" width="30" height="10" fill="#002395" />
          <path d="M0 0L12 10 0 20M12 10H30" stroke="#fff" strokeWidth="7" fill="none" />
          <path d="M0 0L12 10 0 20M12 10H30" stroke="#007749" strokeWidth="4.2" fill="none" />
          <path d="M0 3L8.5 10 0 17Z" fill="#000" />
        </>
      )}
    </svg>
  )
}
