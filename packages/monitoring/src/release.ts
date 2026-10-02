import sha from '../../../internal/platform/buildsha.txt?raw'
import { releaseName } from './release-name'

export { releaseName }

export const RELEASE = releaseName(sha, import.meta.env.VITE_RAILWAY_GIT_COMMIT_SHA ?? '')
