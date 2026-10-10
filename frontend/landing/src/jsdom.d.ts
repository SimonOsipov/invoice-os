// jsdom ships no types; the share test uses only JSDOM and CookieJar.
declare module 'jsdom' {
  export class CookieJar {
    getCookiesSync(url: string): { key: string; value: string }[]
  }
  export class JSDOM {
    constructor(html?: string, options?: { url?: string; cookieJar?: CookieJar })
    window: { document: Document }
  }
}
