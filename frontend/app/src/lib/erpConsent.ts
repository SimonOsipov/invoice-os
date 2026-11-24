// The consent tab of an ERP sign-in, shared by Connect (ConnectorsView) and Reconnect (ConnectorDetail).

import { safeConsentUrl } from './erpConnections'

/** Opened before any await: a window opened after one is a blocked popup. */
export function openConsentTab(): Window | null {
  const tab = window.open('', '_blank')
  if (tab) tab.opener = null
  return tab
}

/** The answered URL, or null (the tab untouched) when it is not an absolute http(s) URL. */
export function sendToConsent(tab: Window | null, answer: { consent_url: string }): string | null {
  const url = safeConsentUrl(answer.consent_url)
  if (url !== null && tab) tab.location.href = url
  return url
}

export function closeConsentTab(tab: Window | null): void {
  tab?.close()
}
