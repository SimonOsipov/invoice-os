import { linkParts } from '../linkParts'

export function Linked({ text }: { text: string }) {
  return (
    <>
      {linkParts(text).map((p, i) =>
        p.href ? (
          <a key={i} href={p.href} target="_blank" rel="noopener noreferrer" style={{ color: 'inherit', textDecoration: 'underline' }}>
            {p.text}
          </a>
        ) : (
          p.text
        ),
      )}
    </>
  )
}
