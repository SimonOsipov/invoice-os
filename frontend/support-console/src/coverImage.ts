import type { CoverPickDeps } from './blogFlow'

// Browser seam for pickCover; the flow around it is tested in blogFlow.test.ts.
export const browserCoverDeps: CoverPickDeps = {
  readAsDataUrl: (file) =>
    new Promise((resolve, reject) => {
      const r = new FileReader()
      r.onload = () => resolve(String(r.result))
      r.onerror = () => reject(r.error)
      r.readAsDataURL(file)
    }),
  loadImage: (src) =>
    new Promise((resolve, reject) => {
      const img = new Image()
      img.onload = () => resolve(img)
      img.onerror = () => reject(new Error('image did not decode'))
      img.src = src
    }),
  toJpeg: (image, width, height, quality) => {
    const canvas = document.createElement('canvas')
    canvas.width = width
    canvas.height = height
    const ctx = canvas.getContext('2d')
    if (!ctx) throw new Error('no 2d canvas')
    ctx.drawImage(image as HTMLImageElement, 0, 0, width, height)
    // JPEG has no alpha: fill behind a transparent PNG so it does not turn black.
    ctx.globalCompositeOperation = 'destination-over'
    ctx.fillStyle = 'white'
    ctx.fillRect(0, 0, width, height)
    return canvas.toDataURL('image/jpeg', quality)
  },
}
