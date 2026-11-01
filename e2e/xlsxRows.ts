import { unzipSync } from 'fflate'

const unescapeXml = (s: string) =>
  s.replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&apos;/g, "'").replace(/&amp;/g, '&')

// excelize's stream writer emits `<row>` of `<c t="inlineStr"><is><t>` cells.
export function sheetRows(xlsx: Uint8Array): string[][] {
  const xml = new TextDecoder().decode(unzipSync(xlsx)['xl/worksheets/sheet1.xml'])
  return [...xml.matchAll(/<row\b[^>]*>(.*?)<\/row>/gs)].map((row) =>
    [...row[1].matchAll(/<c\b[^>]*>(?:<is><t[^>]*>(.*?)<\/t><\/is>)?<\/c>/gs)].map((c) => unescapeXml(c[1] ?? '')),
  )
}
