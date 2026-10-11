// Imported first in main.tsx: ES imports are hoisted, so init must precede the app's module graph.
import { initMonitoring } from '@invoice-os/monitoring'

initMonitoring('library')
